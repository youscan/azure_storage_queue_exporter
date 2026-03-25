package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"
	"github.com/apex/log"
	"github.com/apex/log/handlers/json"
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
	fLogFormat          = flag.String("log.format", "text", "Log format {text|json}")

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

type StorageAccount struct {
	Name   string
	Client *azqueue.ServiceClient
}

type Exporter struct {
	storageAccounts     []StorageAccount
	mMessageCount       *prometheus.GaugeVec
	mMessageTimeInQueue *prometheus.GaugeVec
}

func (e *Exporter) Collect() {
	log.Debug("collection cycle start")
	timeStart := timex.Now()
	var wgA sync.WaitGroup
	wgA.Add(len(e.storageAccounts))
	for _, account := range e.storageAccounts {
		go func(account StorageAccount) {
			defer wgA.Done()
			logCtxA := log.WithFields(log.Fields{"storage_account": account.Name})
			ctx := context.TODO()

			pager := account.Client.NewListQueuesPager(nil)
			var queueNames []string
			for pager.More() {
				resp, err := pager.NextPage(ctx)
				if err != nil {
					logCtxA.WithError(err).Error("failed to list queues in storage account")
					return
				}
				for _, q := range resp.Queues {
					queueNames = append(queueNames, *q.Name)
				}
			}

			logCtxA.Debugf("found %d queues in storage account", len(queueNames))
			var wgQ sync.WaitGroup
			for _, queueName := range queueNames {
				wgQ.Add(1)
				go func(queueName string) {
					defer wgQ.Done()
					logCtxQ := logCtxA.WithFields(log.Fields{"queue": queueName})
					queueClient := account.Client.NewQueueClient(queueName)

					messageCount, err := getMessageCount(ctx, queueClient)
					if err != nil {
						logCtxQ.WithError(err).Error("failed to get queue message count")
						return
					}
					e.mMessageCount.WithLabelValues(account.Name, queueName).Set(float64(messageCount))

					messageTimeInQueue, err := getMessageTimeInQueue(ctx, queueClient)
					if err != nil {
						logCtxQ.WithError(err).Error("failed to get message time in queue")
						return
					}
					e.mMessageTimeInQueue.WithLabelValues(account.Name, queueName).Set(messageTimeInQueue.Seconds())
					logCtxQ.WithFields(log.Fields{"message_count": messageCount, "message_time_in_queue": messageTimeInQueue.String()}).Debug("processed a queue")
				}(queueName)
			}
			wgQ.Wait()
		}(account)
	}
	wgA.Wait()
	scrapeTime := timex.Since(timeStart)
	log.WithFields(log.Fields{"duration": scrapeTime.String()}).Debug("collection cycle end")
	isReady.Store(true)
}

func getMessageCount(ctx context.Context, client *azqueue.QueueClient) (int32, error) {
	props, err := client.GetProperties(ctx, nil)
	if err != nil {
		return -1, err
	}
	if props.ApproximateMessagesCount == nil {
		return 0, nil
	}
	return *props.ApproximateMessagesCount, nil
}

func getMessageTimeInQueue(ctx context.Context, client *azqueue.QueueClient) (time.Duration, error) {
	resp, err := client.PeekMessage(ctx, nil)
	if err != nil {
		return -1, err
	}
	if len(resp.Messages) == 0 {
		return 0, nil
	}
	insertionTime := resp.Messages[0].InsertionTime
	if insertionTime == nil {
		return 0, nil
	}
	return timex.Since(*insertionTime), nil
}

func getStorageAccounts() ([]StorageAccount, error) {
	var accounts []StorageAccount
	var defaultCred *azidentity.DefaultAzureCredential

	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "STORAGE_ACCOUNT_") {
			continue
		}
		s := strings.SplitN(v, "=", 2)
		accountName := strings.TrimPrefix(s[0], "STORAGE_ACCOUNT_")
		accountKey := s[1]
		serviceURL := fmt.Sprintf("https://%s.queue.core.windows.net", accountName)
		logCtx := log.WithFields(log.Fields{"storage_account": accountName})

		var client *azqueue.ServiceClient
		var err error

		if accountKey == "" {
			if defaultCred == nil {
				defaultCred, err = azidentity.NewDefaultAzureCredential(nil)
				if err != nil {
					return nil, fmt.Errorf("failed to create DefaultAzureCredential: %w", err)
				}
			}
			client, err = azqueue.NewServiceClient(serviceURL, defaultCred, nil)
			if err != nil {
				return nil, err
			}
			logCtx.Info("using DefaultAzureCredential for Storage Account")
		} else {
			credential, err := azqueue.NewSharedKeyCredential(accountName, accountKey)
			if err != nil {
				return nil, err
			}
			client, err = azqueue.NewServiceClientWithSharedKeyCredential(serviceURL, credential, nil)
			if err != nil {
				return nil, err
			}
			logCtx.Info("using shared key for Storage Account")
		}

		accounts = append(accounts, StorageAccount{Name: accountName, Client: client})
	}
	return accounts, nil
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

	switch *fLogFormat {
	case "json":
		log.SetHandler(json.New(os.Stderr))
	case "text":
		log.SetHandler(text.New(os.Stderr))
	default:
		log.Fatal("invalid log format")
	}
	log.SetLevelFromString(*fLogLevel)

	collectionInterval, err := time.ParseDuration(*fCollectionInterval)
	if err != nil {
		log.WithError(err).Fatal("failed to parse collection.interval flag")
	}

	storageAccounts, err := getStorageAccounts()
	if err != nil {
		log.WithError(err).Fatal("failed to parse Storage Account credentials")
	}

	go func() {
		prometheus.MustRegister(mMessageCount)
		prometheus.MustRegister(mMessageTimeInQueue)
		log.Info("registered Prometheus metrics")

		exporter := &Exporter{
			storageAccounts:     storageAccounts,
			mMessageCount:       mMessageCount,
			mMessageTimeInQueue: mMessageTimeInQueue,
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
