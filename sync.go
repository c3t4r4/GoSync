package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/schollz/progressbar/v3"
)

// Config struct for source, destination paths, and log file path
type Config struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	LogFile     string `json:"logfile"`
	Worker     int `json:"worker"`
	SkipExtensions []string `json:"skip_extensions"`
}

// ReadConfig reads the config from a JSON file
func ReadConfig(filename string) (Config, error) {
	var config Config
	file, err := os.Open(filename)
	if err != nil {
		return config, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	err = decoder.Decode(&config)
	return config, err
}

// CopyFile copies a file from source to destination
func CopyFile(sourceFile, destFile string) error {
	source, err := os.Open(sourceFile)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.Create(destFile)
	if err != nil {
		return err
	}
	defer destination.Close()

	sourceInfo, err := source.Stat()
	if err != nil {
		return err
	}

	bar := progressbar.NewOptions64(
		sourceInfo.Size(),
		progressbar.OptionSetDescription(fmt.Sprintf("Copying %s", filepath.Base(sourceFile))),
		progressbar.OptionSetWriter(os.Stdout),
		progressbar.OptionShowBytes(true),
		progressbar.OptionShowCount(),
		progressbar.OptionThrottle(65*time.Millisecond),
		progressbar.OptionSetWidth(40),
		progressbar.OptionClearOnFinish(),
	)

	buf := make([]byte, 32*1024) // 32KB buffer
	start := time.Now()
	for {
		n, err := source.Read(buf)
		if n > 0 {
			_, writeErr := destination.Write(buf[:n])
			if writeErr != nil {
				return writeErr
			}
			bar.Add(n)

			elapsed := time.Since(start).Seconds()
			speed := float64(bar.State().CurrentBytes) / elapsed
			bar.Describe(fmt.Sprintf("%s (%.2f KB/s)", filepath.Base(sourceFile), speed/1024))
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}

	return bar.Finish()
}

// FilesAreEqual checks if two files are equal by comparing their size and modification time
func FilesAreEqual(sourceFile, destFile string) (bool, error) {
	sourceInfo, err := os.Stat(sourceFile)
	if err != nil {
		return false, err
	}

	destInfo, err := os.Stat(destFile)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if sourceInfo.Size() != destInfo.Size() {
		return false, nil
	}

	if !sourceInfo.ModTime().Equal(destInfo.ModTime()) {
		return false, nil
	}

	return true, nil
}

// LogCopiedFile logs the copied file to the console and to the log file
func LogCopiedFile(logFile, filePath string, mu *sync.Mutex) error {
	mu.Lock()
	defer mu.Unlock()

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	logEntry := fmt.Sprintf("%s: %s\n", time.Now().Format(time.RFC3339), filePath)
	if _, err := f.WriteString(logEntry); err != nil {
		return err
	}

	fmt.Println(logEntry)
	return nil
}

// Function to check if a file extension is in the skip list
func shouldSkipFile(path string, skipExtensions []string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, skipExt := range skipExtensions {
		if ext == skipExt {
			return true
		}
	}
	return false
}

// Worker function for copying files
func worker(id int, sourceDir string, jobs <-chan string, destDir string, logFile string, skipExtensions []string, wg *sync.WaitGroup, mu *sync.Mutex) {
	defer wg.Done()
	for path := range jobs {
		relativePath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			fmt.Printf("Worker %d: Error getting relative path for %s: %v\n", id, path, err)
			continue
		}

		destPath := filepath.Join(destDir, relativePath)

		// Skip PDF files
		if shouldSkipFile(path, skipExtensions) {
			continue
		}

		// Create directories if needed
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			createDirectory(destPath)
			continue
		}

		// Check if the file already exists and is identical
		equal, err := FilesAreEqual(path, destPath)
		if err != nil {
			fmt.Printf("Worker %d: Error comparing files %s and %s: %v\n", id, path, destPath, err)
			continue
		}

		if equal {
			continue
		}

		// Copy the file
		fmt.Printf("Worker %d: Copying %s to %s\n", id, path, destPath)
		if err := CopyFile(path, destPath); err != nil {
			fmt.Printf("Worker %d: Error copying file %s to %s: %v\n", id, path, destPath, err)
			time.Sleep(30 * time.Second)
			continue
		}

		// Set the modification time of the copied file to match the source
		if info, err := os.Stat(path); err == nil {
			if err := os.Chtimes(destPath, time.Now(), info.ModTime()); err != nil {
				fmt.Printf("Worker %d: Error setting times for %s: %v\n", id, destPath, err)
			}
		}

		// Log the copied file
		if err := LogCopiedFile(logFile, destPath, mu); err != nil {
			fmt.Printf("Worker %d: Error logging file %s: %v\n", id, destPath, err)
		}
	}
}

// SyncDirectories synchronizes files between two directories excluding PDFs using goroutines
func SyncDirectories(sourceDir, destDir, logFile string, workers int, skipExtensions []string) error {
	var wg sync.WaitGroup
	mu := &sync.Mutex{}
	jobs := make(chan string, 100)

	// Start workers
	for w := 1; w <= workers; w++ {
		wg.Add(1)
		go worker(w, sourceDir, jobs, destDir, logFile, skipExtensions, &wg, mu)
	}

	// Walk through the source directory and send jobs to the workers
	err := filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		jobs <- path
		return nil
	})

	close(jobs)
	wg.Wait()
	return err
}

func createDirectory(path string) {
	err := os.MkdirAll(path, os.ModePerm)
	if err != nil {
		fmt.Printf("Error creating directory %s: %v\n", path, err)
		return
	}
}

func main() {
	// Load configuration
	config, err := ReadConfig("config.json")
	if err != nil {
		fmt.Println("Error reading config:", err)
		return
	}

	// Ensure destination directory exists
	if info, err := os.Stat(config.Destination); err == nil && info.IsDir() {
		createDirectory(config.Destination)
	}

	// Synchronize directories
	err = SyncDirectories(config.Source, config.Destination, config.LogFile, config.Worker, config.SkipExtensions)
	if err != nil {
		fmt.Println("Error syncing directories:", err)
	}
}
