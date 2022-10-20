FROM golang:1.19.2-buster

ENV OS linux
ENV GO111MODULE on
ENV PKGPATH github.com/inigohu/pubsub

# copy current workspace
WORKDIR ${GOPATH}/src/${PKGPATH}
COPY . ${GOPATH}/src/${PKGPATH}

RUN go mod download