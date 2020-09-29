FROM golang:1.15

ADD cmd /go/src/github.com/dashpole/dashpole_demos/tracing_app/cmd
WORKDIR /go/src/github.com/dashpole/dashpole_demos/tracing_app
RUN go build cmd/main.go

CMD ["./main"]  