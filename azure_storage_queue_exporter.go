package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-storage-queue-go/azqueue"
	"github.com/apex/log"
	"github.com/apex/log/handlers/text"
	"github.com/cabify/timex"
	_ "github.com/joho/godotenv/autoload"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "azure_queue"

var (
	// CLI flags
	listenAddress = flag.String("web.listen-address", ":9874", "Address to listen on for telemetry")
	metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics")
	logLevel      = flag.String("log.level", "warn", "Log level {debug|info|warn|error|fatal}")

	// Metrics
	descUp = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "exporter", "up"),
		"Was the last Azure Storage Queue query successful.",
		nil, nil,
	)
	descScrapeTime = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "exporter", "scrape_time"),
		"How much time it took to scrape metrics.",
		nil, nil,
	)
	descMessageCount = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "message_count"),
		"How many messages the queue contains.",
		[]string{"storage_account", "queue_name"}, nil,
	)
	descMessageTimeInQueue = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "message_time_in_queue"),
		"How much time a message spent in queue.",
		[]string{"storage_account", "queue_name"}, nil,
	)
)

type Exporter struct {
	storageAccountCredentials []azqueue.SharedKeyCredential
}

func NewExporter(storageAccountCredentials []azqueue.SharedKeyCredential) *Exporter {
	return &Exporter{
		storageAccountCredentials: storageAccountCredentials,
	}
}

func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- descMessageCount
	ch <- descMessageTimeInQueue
}

func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	timeStart := timex.Now()
	var wgA sync.WaitGroup
	wgA.Add(len(e.storageAccountCredentials))
	for _, account := range e.storageAccountCredentials {
		go func(ch chan<- prometheus.Metric, account azqueue.SharedKeyCredential) {
			logCtxA := log.WithFields(log.Fields{"storage_account": account.AccountName()})
			u, _ := url.Parse(fmt.Sprintf("https://%s.queue.core.windows.net", account.AccountName()))
			p := azqueue.NewPipeline(&account, azqueue.PipelineOptions{})
			serviceURL := azqueue.NewServiceURL(*u, p)
			ctx := context.TODO()
			s, err := serviceURL.ListQueuesSegment(ctx, azqueue.Marker{}, azqueue.ListQueuesSegmentOptions{})
			if err != nil {
				ch <- prometheus.MustNewConstMetric(
					descUp, prometheus.GaugeValue, 0,
				)
				logCtxA.WithError(err).Error("failed to list queues in storage account")
				wgA.Done()
				return
			}
			var wgQ sync.WaitGroup
			for _, queueItem := range s.QueueItems {
				wgQ.Add(1)
				queue := Queue{
					Name:       queueItem.Name,
					ServiceURL: serviceURL,
				}
				go func(ch chan<- prometheus.Metric, queue Queue) {
					logCtxQ := logCtxA.WithFields(log.Fields{"queue": queue.Name})
					messageCount, err := queue.getMessageCount()
					if err != nil {
						ch <- prometheus.MustNewConstMetric(
							descUp, prometheus.GaugeValue, 0,
						)
						logCtxQ.WithError(err).Error("failed to get queue message count")
						wgQ.Done()
						return
					}
					ch <- prometheus.MustNewConstMetric(
						descMessageCount, prometheus.GaugeValue, float64(messageCount), account.AccountName(), queue.Name,
					)
					messageTimeInQueue, err := queue.getMessageTimeInQueue()
					if err != nil {
						ch <- prometheus.MustNewConstMetric(
							descUp, prometheus.GaugeValue, 0,
						)
						logCtxQ.WithError(err).Error("failed to get message time in queue")
						wgQ.Done()
						return
					}
					ch <- prometheus.MustNewConstMetric(
						descMessageTimeInQueue, prometheus.GaugeValue, messageTimeInQueue.Seconds(), account.AccountName(), queue.Name,
					)
					logCtxQ.WithFields(log.Fields{"message_count": messageCount, "message_time_in_queue": messageTimeInQueue.String()}).Debug("processed a queue")
					wgQ.Done()
				}(ch, queue)
			}
			wgQ.Wait()
			wgA.Done()
		}(ch, account)
	}
	wgA.Wait()
	ch <- prometheus.MustNewConstMetric(
		descUp, prometheus.GaugeValue, 1,
	)
	scrapeTime := timex.Since(timeStart)
	ch <- prometheus.MustNewConstMetric(
		descScrapeTime, prometheus.GaugeValue, scrapeTime.Seconds(),
	)
	log.WithFields(log.Fields{"duration": scrapeTime.String()}).Debug("collection finished")
}

type Queue struct {
	Name       string
	ServiceURL azqueue.ServiceURL
}

func (q Queue) getMessageCount() (int32, error) {
	ctx := context.TODO()
	queueURL := q.ServiceURL.NewQueueURL(q.Name)
	queueProperties, err := queueURL.GetProperties(ctx)
	if err != nil {
		return -1, err
	}
	messageCount := queueProperties.ApproximateMessagesCount()
	return messageCount, nil
}

func (q Queue) getMessageTimeInQueue() (time.Duration, error) {
	ctx := context.TODO()
	messagesURL := q.ServiceURL.NewQueueURL(q.Name).NewMessagesURL()
	p, err := messagesURL.Peek(ctx, 1)
	if err != nil {
		return time.Duration(-1), err
	}
	var messageTimeInQueue time.Duration
	if p.NumMessages() > 0 {
		insertionTime := p.Message(0).InsertionTime
		messageTimeInQueue = timex.Since(insertionTime)
	}
	return messageTimeInQueue, nil
}

func getStorageAccounts() ([]azqueue.SharedKeyCredential, error) {
	var storageAccounts []azqueue.SharedKeyCredential
	for _, v := range os.Environ() {
		if strings.HasPrefix(v, "STORAGE_ACCOUNT_") {
			s := strings.SplitN(v, "=", 2)
			storageAccountName := strings.TrimPrefix(s[0], "STORAGE_ACCOUNT_")
			storageAccountKey := s[1]
			credential, err := azqueue.NewSharedKeyCredential(storageAccountName, storageAccountKey)
			if err != nil {
				return nil, err
			}
			log.WithFields(log.Fields{"storage_account": storageAccountName}).Info("found Storage Account credentials")
			storageAccounts = append(storageAccounts, *credential)
		}
	}
	return storageAccounts, nil
}

func main() {
	flag.Parse()

	log.SetHandler(text.New(os.Stderr))
	log.SetLevelFromString(*logLevel)

	storageAccountCredentials, err := getStorageAccounts()
	if err != nil {
		log.WithError(err).Fatal("failed to load Storage Account credentials")
	}

	exporter := NewExporter(storageAccountCredentials)
	prometheus.MustRegister(exporter)

	http.Handle(*metricsPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte(`<html>
								  <head><title>Azure Storage Queue Exporter</title></head>
								  <body>
								  <h1>Azure Storage Queue Exporter</h1>
								  <p><a href='` + *metricsPath + `'>Metrics</a></p>
								  </body>
								  </html>`))
		if err != nil {
			log.WithError(err).Error("failed writing http response")
		}
	})
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte("ok"))
		if err != nil {
			log.WithError(err).Error("failed writing http response")
		}
	})
	log.WithError(http.ListenAndServe(*listenAddress, nil)).Fatal("http server error")
}
