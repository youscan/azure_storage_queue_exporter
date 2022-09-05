# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.2.2] - 2022-09-05

## Fixed

- Fixed missing queue metrics due to pagination

## Added

- Added JSON logging

## [1.2.1] - 2022-05-02

## Changed

- Tune readiness probe for slow startups

## [1.2.0] - 2021-12-29

## Changed

- Metrics collection now starts immediately instead of after `collection.interval`

## Added

- Added liveness (`/healthz`) and readiness (`/readyz`) probe endpoints: app becomes live on HTTP server startup and becomes ready after first successful metric collection cycle

## [1.1.0] - 2021-12-29

## Changed

- Decoupled metric collection from HTTP handler
- Metrics collection is now triggered by ticker (every 5s by default)

## Added

- Added `--collection.interval` CLI flag for customizing metrics collection interval

## Removed

- Removed `azure_queue_exporter_up` metric
- Removed `azure_queue_exporter_scrape_time` metric

## [1.0.0] - 2021-12-24

## Added

- Initial release

[unreleased]: https://github.com/youscan/azure_storage_queue_exporter/compare/v1.2.2...HEAD
[1.2.2]: https://github.com/youscan/azure_storage_queue_exporter/compare/v1.2.1...v1.2.2
[1.2.1]: https://github.com/youscan/azure_storage_queue_exporter/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/youscan/azure_storage_queue_exporter/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/youscan/azure_storage_queue_exporter/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/youscan/azure_storage_queue_exporter/compare/02d1ad2...v1.0.0
