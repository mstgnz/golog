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
	// Parse command line flags
	levelFilter := flag.String("level", "", "Filter logs by level (INFO, WARNING, ERROR, DEBUG)")
	typeFilter := flag.String("type", "", "Filter logs by type (SYSTEM, AUTH, DATABASE, USER, API)")
	flag.Parse()

	// Load .env file if it exists
	err := godotenv.Load()
	if err != nil {
		log.Println("Warning: .env file not found, using environment variables")
	}

	// Connect to database
	err = database.Connect()
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Channel to receive logs
	logChan := make(chan models.Log)

	// Start log listener
	err = database.ListenForLogs(ctx, logChan)
	if err != nil {
		log.Fatalf("Failed to start log listener: %v", err)
	}

	// Channel to listen for interrupt signals
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Print initial logs
	filter := models.LogFilter{
		Level: *levelFilter,
		Type:  *typeFilter,
	}

	logs, err := database.GetLogs(filter)
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
		// Print logs in reverse order (newest first)
		for i := len(logs) - 1; i >= 0; i-- {
			printLog(logs[i])
		}
	}

	fmt.Println("======================================")
	fmt.Println("Listening for new logs... (Press Ctrl+C to exit)")

	// Listen for new logs
	go func() {
		for logEntry := range logChan {
			// Apply filters
			if (*levelFilter == "" || logEntry.Level == *levelFilter) &&
				(*typeFilter == "" || logEntry.Type == *typeFilter) {
				printLog(logEntry)
			}
		}
	}()

	// Wait for interrupt signal
	<-stop
	fmt.Println("\nShutting down...")
}

func printLog(logEntry models.Log) {
	timestamp := logEntry.Timestamp.Format("2006-01-02 15:04:05")

	// Color codes
	var levelColor string
	switch logEntry.Level {
	case "INFO":
		levelColor = "\033[32m" // Green
	case "WARNING":
		levelColor = "\033[33m" // Yellow
	case "ERROR":
		levelColor = "\033[31m" // Red
	case "DEBUG":
		levelColor = "\033[36m" // Cyan
	default:
		levelColor = "\033[0m" // Default
	}

	resetColor := "\033[0m"

	fmt.Printf("[%s] %s%s%s [%s]: %s\n",
		timestamp,
		levelColor,
		logEntry.Level,
		resetColor,
		logEntry.Type,
		logEntry.Message)
}
