# PHP + Go + Composer, for kest's PHP tests on Linux (make test-matrix).
ARG PHP_VERSION=8.5
ARG GO_IMAGE=golang:1.27

FROM ${GO_IMAGE} AS go
FROM composer:2 AS composer

FROM php:${PHP_VERSION}-cli
# roadrunner/worker needs ext-sockets; composer needs git and unzip for dist installs.
RUN apt-get update \
    && apt-get install -y --no-install-recommends git unzip \
    && rm -rf /var/lib/apt/lists/* \
    && docker-php-ext-install sockets
COPY --from=go /usr/local/go /usr/local/go
COPY --from=composer /usr/bin/composer /usr/bin/composer
ENV PATH=/usr/local/go/bin:/root/go/bin:$PATH \
    GOFLAGS=-buildvcs=false \
    COMPOSER_ALLOW_SUPERUSER=1
COPY run.sh /usr/local/bin/kest-test
ENTRYPOINT ["kest-test"]
