FROM golang:1.8-alpine
RUN apk add -U git
COPY . /go/src/github.com/LK4D4/vndr
RUN go install github.com/LK4D4/vndr
