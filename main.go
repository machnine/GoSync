package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

type Config struct {
	SourceDir string `json:"source_dir"`
	TargetDir string `json:"target_dir"`
}

var logFile *os.File
var wg sync.WaitGroup
var mu sync.Mutex

func main() {
	configFile := flag.String("config", "", "Path to configuration file")
	flag.Parse()

	// Get paths
	var sourceDir, targetDir string
	executablePath, err := os.Executable()
	if err != nil {
		fmt.Printf("Error getting executable path: %v\n", err)
		return
	}
	executableDir := filepath.Dir(executablePath)
	defaultConfigPath := filepath.Join(executableDir, "config.json")

	// Load the configuration file
	if *configFile != "" {
		sourceDir, targetDir, err = loadConfig(*configFile)
	} else if fileExists(defaultConfigPath) {
		sourceDir, targetDir, err = loadConfig(defaultConfigPath)
	} else {
		fmt.Println("Configuration file not found. Please provide the path to the configuration file:")
		reader := bufio.NewReader(os.Stdin)
		configPath, _ := reader.ReadString('\n')
		configPath = strings.TrimSpace(configPath)
		sourceDir, targetDir, err = loadConfig(configPath)
	}

	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		return
	}

	if sourceDir == "" || targetDir == "" {
		fmt.Println("Source and target directories must be specified in the configuration file.")
		return
	}

	// Logging
	logFilePath := filepath.Join(executableDir, "sync.log")
	logFile, err = os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("Error opening log file: %v\n", err)
		return
	}
	defer logFile.Close()

	fmt.Printf("Starting sync from [%s] ===========> [%s]\n", sourceDir, targetDir)

	// Walk through the source directory
	err = filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		wg.Add(1)
		go func() {
			defer wg.Done()

			// Construct the target path
			relPath, err := filepath.Rel(sourceDir, path)
			if err != nil {
				logMessage(fmt.Sprintf("Error getting relative path: %v", err))
				return
			}
			targetPath := filepath.Join(targetDir, relPath)

			// Check if the file needs to be copied
			if shouldCopyFile(path, targetPath, info) {
				err := copyFile(path, targetPath, info)
				if err != nil {
					logMessage(fmt.Sprintf("Error copying file: %v", err))
					return
				}
				logMessage(fmt.Sprintf("Copied: %s", filepath.Base(path)))
			}
		}()

		return nil
	})

	if err != nil {
		logMessage(fmt.Sprintf("Error walking the path: %v", err))
	}

	wg.Wait()
	logMessage("--------------------")
	fmt.Println("Sync completed.")
}

func loadConfig(path string) (string, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()

	var config Config
	err = json.NewDecoder(file).Decode(&config)
	if err != nil {
		return "", "", err
	}

	return config.SourceDir, config.TargetDir, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

func shouldCopyFile(sourcePath, targetPath string, sourceInfo os.FileInfo) bool {
	targetInfo, err := os.Stat(targetPath)
	if os.IsNotExist(err) {
		// File doesn't exist, so we need to copy it
		return true
	} else if err != nil {
		fmt.Printf("Error: %v\n", err)
		return false
	}

	// Check if the source file has been modified after the target file
	return sourceInfo.ModTime().After(targetInfo.ModTime())
}

func copyFile(sourcePath, targetPath string, sourceInfo os.FileInfo) error {
	// Create the target directory if it doesn't exist
	targetDir := filepath.Dir(targetPath)
	err := os.MkdirAll(targetDir, os.ModePerm)
	if err != nil {
		return err
	}

	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	targetFile, err := os.Create(targetPath)
	if err != nil {
		return err
	}
	defer targetFile.Close()

	_, err = io.Copy(targetFile, sourceFile)
	if err != nil {
		return err
	}

	// Explicitly sync the file to ensure all changes are flushed to disk
	err = targetFile.Sync()
	if err != nil {
		return err
	}
	// Preserve the timestamps of the source file
	err = setFileTimes(targetPath, sourceInfo)
	if err != nil {
		return err
	}

	return nil
}

func setFileTimes(targetPath string, sourceInfo os.FileInfo) error {
	stat := sourceInfo.Sys().(*syscall.Win32FileAttributeData)

	// Convert times to windows.Filetime
	creationTime := windows.NsecToFiletime(stat.CreationTime.Nanoseconds())
	lastAccessTime := windows.NsecToFiletime(stat.LastAccessTime.Nanoseconds())
	lastWriteTime := windows.NsecToFiletime(stat.LastWriteTime.Nanoseconds())

	handle, err := windows.CreateFile(
		windows.StringToUTF16Ptr(targetPath),
		windows.FILE_WRITE_ATTRIBUTES,
		windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)

	// Set the file times
	err = windows.SetFileTime(handle, &creationTime, &lastAccessTime, &lastWriteTime)
	if err != nil {
		return err
	}

	return nil
}

func countFiles(dir string) (int, error) {
	count := 0
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			count++
		}
		return nil
	})
	return count, err
}

func logMessage(message string) {
	mu.Lock()
	defer mu.Unlock()
	timestamp := time.Now().Format(time.RFC3339)
	logEntry := fmt.Sprintf("%s - %s\n", timestamp, message)
	logFile.WriteString(logEntry)
}
