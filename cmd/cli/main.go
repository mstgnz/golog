package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/mstgnz/golog/database"
	"github.com/mstgnz/golog/models"
)

func main() {
	levelFilter := flag.String("level", "", "Filter logs by level (INFO, WARNING, ERROR, DEBUG)")
	typeFilter := flag.String("type", "", "Filter logs by type (SYSTEM, AUTH, DATABASE, USER, API)")
	flag.Parse()

	if err := godotenv.Load(); err != nil {
		log.Println("Warning: .env file not found, using environment variables")
	}

	if err := database.Connect(); err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := database.NewStore()
	logChan := make(chan models.Log)

	if err := store.ListenForLogs(ctx, logChan); err != nil {
		log.Fatalf("Failed to start log listener: %v", err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	filter := models.LogFilter{
		Level: *levelFilter,
		Type:  *typeFilter,
	}

	logs, err := store.GetLogs(filter)
	if err != nil {
		log.Fatalf("Failed to get logs: %v", err)
	}

	fmt.Println("=== GoLog - Real-time Log Monitoring ===")
	if *levelFilter != "" {
		fmt.Printf("Level filter: %s\n", *levelFilter)
	}
	if *typeFilter != "" {
		fmt.Printf("Type filter: %s\n", *typeFilter)
	}
	fmt.Println("======================================")

	if len(logs) == 0 {
		fmt.Println("No logs found")
	} else {
		for i := len(logs) - 1; i >= 0; i-- {
			printLog(logs[i])
		}
	}

	fmt.Println("======================================")
	fmt.Println("Listening for new logs... (Press Ctrl+C to exit)")

	go func() {
		for logEntry := range logChan {
			if (*levelFilter == "" || logEntry.Level == *levelFilter) &&
				(*typeFilter == "" || logEntry.Type == *typeFilter) {
				printLog(logEntry)
			}
		}
	}()

	<-stop
	fmt.Println("\nShutting down...")
}

func printLog(logEntry models.Log) {
	timestamp := logEntry.Timestamp.Format("2006-01-02 15:04:05")

	var levelColor string
	switch logEntry.Level {
	case models.LevelInfo:
		levelColor = "\033[32m"
	case models.LevelWarning:
		levelColor = "\033[33m"
	case models.LevelError:
		levelColor = "\033[31m"
	case models.LevelDebug:
		levelColor = "\033[36m"
	default:
		levelColor = "\033[0m"
	}

	fmt.Printf("[%s] %s%s\033[0m [%s]: %s\n",
		timestamp, levelColor, logEntry.Level, logEntry.Type, logEntry.Message)
}
