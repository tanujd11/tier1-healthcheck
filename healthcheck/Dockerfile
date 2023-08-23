FROM golang:1.20 as builder

ENV APP_HOME /go/src/healthcheck

WORKDIR "$APP_HOME"

COPY . .

RUN go mod download
RUN go mod verify
RUN go build -o healthcheck

FROM golang:1.20

ENV APP_HOME /go/src/healthcheck
RUN mkdir -p "$APP_HOME"
WORKDIR "$APP_HOME"

COPY --from=builder "$APP_HOME"/healthcheck $APP_HOME

COPY wildcard.crt $APP_HOME
COPY wildcard.key $APP_HOME
COPY wildcard-ca.crt $APP_HOME

EXPOSE 9443
CMD ["./healthcheck"]