FROM example.com/base/golang:1.22 AS build
WORKDIR /src
COPY . .
RUN go build -o /out/worker ./modules/worker/cmd

FROM example.com/base/debian:12
LABEL org.example.harbor.version="1.4.2"
COPY --from=build /out/worker /usr/local/bin/worker
CMD ["worker"]
