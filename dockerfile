FROM golang:1.22-alpine
WORKDIR /app
COPY . .
RUN go build -o golog-server cmd/main.go
RUN go build -o golog-cli cmd/cli/main.go
CMD ["./golog-server"]