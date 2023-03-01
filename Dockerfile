FROM golang:1.20.1-alpine AS build
WORKDIR /go/src/github.com/youscan/azure_storage_queue_exporter
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go install -a -installsuffix cgo .

FROM alpine:3
COPY --from=build /go/bin/azure_storage_queue_exporter /bin/azure_storage_queue_exporter
ENTRYPOINT ["/bin/azure_storage_queue_exporter"]
