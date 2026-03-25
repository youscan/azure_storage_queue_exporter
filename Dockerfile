FROM golang:1.26.1-alpine AS build
WORKDIR /go/src/github.com/youscan/azure_storage_queue_exporter
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go install -a -installsuffix cgo .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /go/bin/azure_storage_queue_exporter /azure_storage_queue_exporter
USER 65532:65532
ENTRYPOINT ["/azure_storage_queue_exporter"]
