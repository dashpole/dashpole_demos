FROM golang:1.17

ADD cmd /go/src/github.com/dashpole/dashpole_demos/removeiftest/cmd
WORKDIR /go/src/github.com/dashpole/dashpole_demos/removeiftest/cmd
RUN go build main.go

CMD ["./main"]  