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
	"sync/atomic"
	"time"

	"github.com/Azure/azure-storage-queue-go/azqueue"
	"github.com/apex/log"
	"github.com/apex/log/handlers/text"
	"github.com/cabify/timex"
	_ "github.com/joho/godotenv/autoload"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// CLI flags
	fCollectionInterval = flag.String("collection.interval", "5s", "Metric collection interval")
	fListenAddress      = flag.String("web.listen-address", ":9874", "Address to listen on for telemetry")
	fMetricsPath        = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics")
	fLogLevel           = flag.String("log.level", "warn", "Log level {debug|info|warn|error|fatal}")

	// Metrics
	mMessageCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "azure_queue_message_count",
		Help: "How many messages the queue contains.",
	}, []string{"storage_account", "queue_name"})
	mMessageTimeInQueue = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "azure_queue_message_time_in_queue",
		Help: "How many seconds a message spent in queue.",
	}, []string{"storage_account", "queue_name"})

	// Readiness probe flag
	isReady = &atomic.Value{}
)

type Exporter struct {
	storageAccountCredentials []azqueue.SharedKeyCredential
	mMessageCount             *prometheus.GaugeVec
	mMessageTimeInQueue       *prometheus.GaugeVec
}

func (e *Exporter) Collect() {
	log.Debug("collection cycle start")
	timeStart := timex.Now()
	var wgA sync.WaitGroup
	wgA.Add(len(e.storageAccountCredentials))
	for _, account := range e.storageAccountCredentials {
		go func(account azqueue.SharedKeyCredential) {
			logCtxA := log.WithFields(log.Fields{"storage_account": account.AccountName()})
			u, _ := url.Parse(fmt.Sprintf("https://%s.queue.core.windows.net", account.AccountName()))
			p := azqueue.NewPipeline(&account, azqueue.PipelineOptions{})
			serviceURL := azqueue.NewServiceURL(*u, p)
			ctx := context.TODO()
			m := azqueue.Marker{}
			queueItems := []azqueue.QueueItem{}
			for m.NotDone() {
				s, err := serviceURL.ListQueuesSegment(ctx, m, azqueue.ListQueuesSegmentOptions{})
				if err != nil {
					logCtxA.WithError(err).Error("failed to list queues in storage account")
					wgA.Done()
					return
				}
				queueItems = append(queueItems, s.QueueItems...)
				m = s.NextMarker
			}
			logCtxA.Debugf("found %d queues in storage account", len(queueItems))
			var wgQ sync.WaitGroup
			for _, queueItem := range queueItems {
				wgQ.Add(1)
				queue := Queue{
					Name:       queueItem.Name,
					ServiceURL: serviceURL,
				}
				go func(queue Queue) {
					logCtxQ := logCtxA.WithFields(log.Fields{"queue": queue.Name})
					messageCount, err := queue.getMessageCount()
					if err != nil {
						logCtxQ.WithError(err).Error("failed to get queue message count")
						wgQ.Done()
						return
					}
					e.mMessageCount.WithLabelValues(account.AccountName(), queue.Name).Set(float64(messageCount))
					messageTimeInQueue, err := queue.getMessageTimeInQueue()
					if err != nil {
						logCtxQ.WithError(err).Error("failed to get message time in queue")
						wgQ.Done()
						return
					}
					e.mMessageTimeInQueue.WithLabelValues(account.AccountName(), queue.Name).Set(messageTimeInQueue.Seconds())
					logCtxQ.WithFields(log.Fields{"message_count": messageCount, "message_time_in_queue": messageTimeInQueue.String()}).Debug("processed a queue")
					wgQ.Done()
				}(queue)
			}
			wgQ.Wait()
			wgA.Done()
		}(account)
	}
	wgA.Wait()
	scrapeTime := timex.Since(timeStart)
	log.WithFields(log.Fields{"duration": scrapeTime.String()}).Debug("collection cycle end")
	isReady.Store(true)
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

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func readyz(isReady *atomic.Value) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if isReady == nil || !isReady.Load().(bool) {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func main() {
	isReady.Store(false)
	flag.Parse()

	log.SetHandler(text.New(os.Stderr))
	log.SetLevelFromString(*fLogLevel)

	collectionInterval, err := time.ParseDuration(*fCollectionInterval)
	if err != nil {
		log.WithError(err).Fatal("failed to parse collection.interval flag")
	}

	storageAccountCredentials, err := getStorageAccounts()
	if err != nil {
		log.WithError(err).Fatal("failed to parse Storage Account credentials")
	}

	go func() {
		prometheus.MustRegister(mMessageCount)
		prometheus.MustRegister(mMessageTimeInQueue)
		log.Info("registered Prometheus metrics")

		exporter := &Exporter{
			storageAccountCredentials: storageAccountCredentials,
			mMessageCount:             mMessageCount,
			mMessageTimeInQueue:       mMessageTimeInQueue,
		}
		ticker := time.NewTicker(collectionInterval)
		defer ticker.Stop()

		log.WithField("interval", collectionInterval.String()).Info("starting collection")
		for ; true; <-ticker.C {
			go exporter.Collect()
		}
	}()

	http.Handle(*fMetricsPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte(`<html>
								  <head><title>Azure Storage Queue Exporter</title></head>
								  <body>
								  <h1>Azure Storage Queue Exporter</h1>
								  <p><a href='` + *fMetricsPath + `'>Metrics</a></p>
								  </body>
								  </html>`))
		if err != nil {
			log.WithError(err).Error("failed writing http response")
		}
	})
	http.HandleFunc("/healthz", healthz)
	http.HandleFunc("/readyz", readyz(isReady))
	log.WithError(http.ListenAndServe(*fListenAddress, nil)).Fatal("http server error")
}
