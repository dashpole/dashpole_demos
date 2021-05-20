FROM golang:1.15

ADD cmd /go/src/github.com/dashpole/dashpole_demos/load_testing/prometheus/cmd
WORKDIR /go/src/github.com/dashpole/dashpole_demos/load_testing/prometheus
RUN go build cmd/main.go

CMD ["./main"]  