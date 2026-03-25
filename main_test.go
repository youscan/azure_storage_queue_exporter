package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/azure/azurite"
)

func setupAzurite(t *testing.T) (*azqueue.ServiceClient, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := azurite.Run(ctx,
		"mcr.microsoft.com/azure-storage/azurite:3.34.0",
		azurite.WithInMemoryPersistence(64),
	)
	if err != nil {
		t.Fatalf("failed to start azurite: %v", err)
	}

	queueServiceURL, err := container.QueueServiceURL(ctx)
	if err != nil {
		_ = testcontainers.TerminateContainer(container)
		t.Fatalf("failed to get queue service URL: %v", err)
	}

	connStr := fmt.Sprintf(
		"DefaultEndpointsProtocol=http;AccountName=%s;AccountKey=%s;QueueEndpoint=%s/%s;",
		azurite.AccountName, azurite.AccountKey, queueServiceURL, azurite.AccountName,
	)
	client, err := azqueue.NewServiceClientFromConnectionString(connStr, nil)
	if err != nil {
		_ = testcontainers.TerminateContainer(container)
		t.Fatalf("failed to create service client: %v", err)
	}

	cleanup := func() {
		_ = testcontainers.TerminateContainer(container)
	}
	return client, cleanup
}

func createQueue(t *testing.T, client *azqueue.ServiceClient, name string) {
	t.Helper()
	if _, err := client.CreateQueue(context.Background(), name, nil); err != nil {
		t.Fatalf("failed to create queue %s: %v", name, err)
	}
}

func enqueueMessage(t *testing.T, client *azqueue.ServiceClient, queueName, message string) {
	t.Helper()
	queueClient := client.NewQueueClient(queueName)
	if _, err := queueClient.EnqueueMessage(context.Background(), message, nil); err != nil {
		t.Fatalf("failed to enqueue message to %s: %v", queueName, err)
	}
}

func TestGetMessageCount(t *testing.T) {
	client, cleanup := setupAzurite(t)
	defer cleanup()

	createQueue(t, client, "test-count")
	enqueueMessage(t, client, "test-count", "msg1")
	enqueueMessage(t, client, "test-count", "msg2")
	enqueueMessage(t, client, "test-count", "msg3")

	queueClient := client.NewQueueClient("test-count")
	count, err := getMessageCount(context.Background(), queueClient)
	if err != nil {
		t.Fatalf("getMessageCount failed: %v", err)
	}
	if count != 3 {
		t.Errorf("expected message count 3, got %d", count)
	}
}

func TestGetMessageCountEmptyQueue(t *testing.T) {
	client, cleanup := setupAzurite(t)
	defer cleanup()

	createQueue(t, client, "test-empty")

	queueClient := client.NewQueueClient("test-empty")
	count, err := getMessageCount(context.Background(), queueClient)
	if err != nil {
		t.Fatalf("getMessageCount failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected message count 0, got %d", count)
	}
}

func TestGetMessageTimeInQueue(t *testing.T) {
	client, cleanup := setupAzurite(t)
	defer cleanup()

	createQueue(t, client, "test-time")
	enqueueMessage(t, client, "test-time", "msg1")

	queueClient := client.NewQueueClient("test-time")
	duration, err := getMessageTimeInQueue(context.Background(), queueClient)
	if err != nil {
		t.Fatalf("getMessageTimeInQueue failed: %v", err)
	}
	if duration < 0 {
		t.Errorf("expected non-negative duration, got %v", duration)
	}
}

func TestGetMessageTimeInQueueEmpty(t *testing.T) {
	client, cleanup := setupAzurite(t)
	defer cleanup()

	createQueue(t, client, "test-time-empty")

	queueClient := client.NewQueueClient("test-time-empty")
	duration, err := getMessageTimeInQueue(context.Background(), queueClient)
	if err != nil {
		t.Fatalf("getMessageTimeInQueue failed: %v", err)
	}
	if duration != 0 {
		t.Errorf("expected zero duration for empty queue, got %v", duration)
	}
}

func TestCollect(t *testing.T) {
	client, cleanup := setupAzurite(t)
	defer cleanup()

	createQueue(t, client, "queue-a")
	createQueue(t, client, "queue-b")
	enqueueMessage(t, client, "queue-a", "msg1")
	enqueueMessage(t, client, "queue-a", "msg2")
	enqueueMessage(t, client, "queue-b", "msg1")

	msgCount := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "test_collect_message_count",
	}, []string{"storage_account", "queue_name"})
	msgTime := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "test_collect_message_time",
	}, []string{"storage_account", "queue_name"})

	exporter := &Exporter{
		storageAccounts: []StorageAccount{
			{Name: "teststorage", Client: client},
		},
		mMessageCount:       msgCount,
		mMessageTimeInQueue: msgTime,
	}

	exporter.Collect()

	// Verify metrics were set
	mA := &dto.Metric{}
	mB := &dto.Metric{}
	if err := msgCount.WithLabelValues("teststorage", "queue-a").(prometheus.Metric).Write(mA); err != nil {
		t.Fatalf("failed to read metric: %v", err)
	}
	if err := msgCount.WithLabelValues("teststorage", "queue-b").(prometheus.Metric).Write(mB); err != nil {
		t.Fatalf("failed to read metric: %v", err)
	}

	if mA.GetGauge().GetValue() != 2 {
		t.Errorf("expected queue-a count 2, got %v", mA.GetGauge().GetValue())
	}
	if mB.GetGauge().GetValue() != 1 {
		t.Errorf("expected queue-b count 1, got %v", mB.GetGauge().GetValue())
	}
}

func TestGetStorageAccounts(t *testing.T) {
	t.Setenv("STORAGE_ACCOUNT_testaccount1", "dGVzdGtleTE=")
	t.Setenv("STORAGE_ACCOUNT_testaccount2", "dGVzdGtleTI=")

	accounts, err := getStorageAccounts()
	if err != nil {
		t.Fatalf("getStorageAccounts failed: %v", err)
	}

	if len(accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(accounts))
	}

	names := map[string]bool{}
	for _, a := range accounts {
		names[a.Name] = true
	}
	if !names["testaccount1"] || !names["testaccount2"] {
		t.Errorf("expected testaccount1 and testaccount2, got %v", names)
	}
}

func TestGetStorageAccountsMixed(t *testing.T) {
	t.Setenv("STORAGE_ACCOUNT_keyaccount", "dGVzdGtleTE=")
	t.Setenv("STORAGE_ACCOUNT_identityaccount", "")

	accounts, err := getStorageAccounts()
	if err != nil {
		t.Fatalf("getStorageAccounts failed: %v", err)
	}

	if len(accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(accounts))
	}

	names := map[string]bool{}
	for _, a := range accounts {
		names[a.Name] = true
	}
	if !names["keyaccount"] || !names["identityaccount"] {
		t.Errorf("expected keyaccount and identityaccount, got %v", names)
	}
}

func TestHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	healthz(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name       string
		ready      bool
		wantStatus int
	}{
		{"not ready", false, http.StatusServiceUnavailable},
		{"ready", true, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready := &atomic.Value{}
			ready.Store(tt.ready)

			handler := readyz(ready)
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			w := httptest.NewRecorder()
			handler(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("expected status %d, got %d", tt.wantStatus, w.Code)
			}
		})
	}
}

func TestMultipleQueuesMessageCount(t *testing.T) {
	client, cleanup := setupAzurite(t)
	defer cleanup()

	for i := range 5 {
		name := fmt.Sprintf("multi-queue-%d", i)
		createQueue(t, client, name)
		for j := range i + 1 {
			enqueueMessage(t, client, name, fmt.Sprintf("msg-%d", j))
		}
	}

	for i := range 5 {
		queueClient := client.NewQueueClient(fmt.Sprintf("multi-queue-%d", i))
		count, err := getMessageCount(context.Background(), queueClient)
		if err != nil {
			t.Fatalf("getMessageCount failed for queue %d: %v", i, err)
		}
		expected := int32(i + 1)
		if count != expected {
			t.Errorf("queue %d: expected count %d, got %d", i, expected, count)
		}
	}
}

func TestMessageTimeInQueueIncreases(t *testing.T) {
	client, cleanup := setupAzurite(t)
	defer cleanup()

	createQueue(t, client, "test-time-increase")
	enqueueMessage(t, client, "test-time-increase", "msg1")

	queueClient := client.NewQueueClient("test-time-increase")

	d1, err := getMessageTimeInQueue(context.Background(), queueClient)
	if err != nil {
		t.Fatalf("first getMessageTimeInQueue failed: %v", err)
	}

	time.Sleep(1 * time.Second)

	d2, err := getMessageTimeInQueue(context.Background(), queueClient)
	if err != nil {
		t.Fatalf("second getMessageTimeInQueue failed: %v", err)
	}

	if d2 <= d1 {
		t.Errorf("expected duration to increase, got d1=%v d2=%v", d1, d2)
	}
}
