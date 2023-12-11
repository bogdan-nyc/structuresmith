package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"text/template"
	"time"

	"github.com/alexflint/go-arg"
	"gopkg.in/yaml.v3"
)

// Global variables for application metadata.
var (
	Version   string              // Version of the application.
	Revision  string              // Revision or Commit this binary was built from.
	GoVersion = runtime.Version() // GoVersion running this binary.
	StartTime = time.Now()        // StartTime of the application.
)

// CLIArgs represents command line arguments for the application.
type CLIArgs struct {
	ConfigFile   string `arg:"-c,--config, required, help:Path to the YAML configuration file"`
	OutputPath   string `arg:"-o,--output, help:Output path prefix for generated files"`
	TemplatesDir string `arg:"-t,--templates, help:Directory where template files are stored"`
	Repo         string `arg:"-r,--repo, help:Specify a single repository to render"`
	MaxParallel  int    `arg:"-p,--max-parallel, help:Maximum number of repositories to process in parallel"`
}

// Version returns a formatted string with application version details.
func (CLIArgs) Version() string {
	return fmt.Sprintf("Version: %s %s\nBuildTime: %s\n%s\n", Revision, Version, StartTime.Format("2006-01-02"), GoVersion)
}

// Config represents the structure of the configuration file.
type Config struct {
	TemplateGroups map[string][]FileStructure `yaml:"templateGroups"`
	Repositories   []RepositoryConfig         `yaml:"repositories"`
}

// RepositoryConfig defines the configuration of a single repository.
type RepositoryConfig struct {
	Name   string             `yaml:"name"`
	Files  []FileStructure    `yaml:"files"`
	Groups []TemplateGroupRef `yaml:"groups"`
}

// TemplateGroupRef links a template group with specific values.
type TemplateGroupRef struct {
	GroupName string                 `yaml:"groupName"`
	Values    map[string]interface{} `yaml:"values"`
}

// FileStructure describes a file to be created from a template or URL.
type FileStructure struct {
	Filename   string `yaml:"filename"`
	SourceFile string `yaml:"sourceFile"`
	SourceURL  string `yaml:"sourceUrl"`
	Content    string `yaml:"content"`
	Values     map[string]interface{}
}

// Template represents a template consisting of multiple files.
type Template struct {
	Files []FileStructure
}

// TemplateGroup is a collection of templates.
type TemplateGroup []Template

// Repository represents a repository with associated templates and groups.
type Repository struct {
	Name   string
	Files  []FileStructure
	Groups []TemplateGroupRef
}

// main is the entry point of the application.
func main() {
	var args CLIArgs
	args.OutputPath = "out"         // Default output path
	args.TemplatesDir = "templates" // Default templates directory
	args.MaxParallel = 5            // Default maximum parallel processing

	arg.MustParse(&args)

	// Validate MaxParallel value
	if args.MaxParallel <= 0 {
		log.Fatalf("Invalid max-parallel value: %d. It must be greater than 0.", args.MaxParallel)
	}

	log.Println("Reading configuration...")
	config, err := readConfig(args.ConfigFile, args.TemplatesDir)
	if err != nil {
		log.Fatalf("Error reading config: %v\n", err)
	}

	log.Println("Validating configuration...")
	if err := validateConfig(config); err != nil {
		log.Fatalf("Configuration validation error: %v\n", err)
	}

	startTime := time.Now()

	log.Printf("Processing repositories in parallel (max-parallel: %d) ...", args.MaxParallel)
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, args.MaxParallel) // Semaphore to limit parallelism

	for _, repoConfig := range config.Repositories {
		if args.Repo != "" && repoConfig.Name != args.Repo {
			continue
		}
		wg.Add(1)
		semaphore <- struct{}{} // Acquire semaphore

		go func(rc RepositoryConfig) {
			defer wg.Done()
			defer func() { <-semaphore }() // Release semaphore

			log.Printf("Processing repository: %s\n", rc.Name)
			repo := Repository(rc)
			if err := processRepository(repo, config.TemplateGroups, args.OutputPath); err != nil {
				log.Printf("Error processing repository %s: %v\n", repo.Name, err)
			}
		}(repoConfig)
	}

	wg.Wait() // Wait for all goroutines to finish

	duration := time.Since(startTime)
	log.Printf("Parallel processing completed in %.2fs. Have a great day!\n", duration.Seconds())
}

// readConfig reads and parses the YAML configuration file.
func readConfig(filename, templatesDir string) (Config, error) {
	var config Config
	data, err := os.ReadFile(filename)
	if err != nil {
		return config, fmt.Errorf("failed to read file: %w", err)
	}

	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return config, fmt.Errorf("failed to unmarshal YAML: %w", err)
	}

	for _, group := range config.TemplateGroups {
		for i, file := range group {
			if file.SourceFile != "" {
				group[i].SourceFile = filepath.Join(templatesDir, file.SourceFile)
			}
		}
	}
	log.Println("Configuration read successfully.")
	return config, nil
}

// validateConfig performs various checks on the configuration.
func validateConfig(config Config) error {
	if err := validateDuplicateRepoNames(config.Repositories); err != nil {
		return err
	}
	if err := validateDuplicateTemplateGroups(config.TemplateGroups); err != nil {
		return err
	}
	if err := validateFileStructures(config.TemplateGroups); err != nil {
		return err
	}
	if err := validateRepoGroupReferences(config.Repositories, config.TemplateGroups); err != nil {
		return err
	}
	if err := validateURLSchemes(config.TemplateGroups); err != nil {
		return err
	}
	return nil
}

// validateDuplicateRepoNames checks for duplicate repository names.
func validateDuplicateRepoNames(repos []RepositoryConfig) error {
	repoNames := make(map[string]bool)
	for _, repo := range repos {
		if _, exists := repoNames[repo.Name]; exists {
			return fmt.Errorf("duplicate repository name: %s", repo.Name)
		}
		repoNames[repo.Name] = true
	}
	return nil
}

// validateDuplicateTemplateGroups checks for duplicate template groups.
func validateDuplicateTemplateGroups(groups map[string][]FileStructure) error {
	groupNames := make(map[string]bool)
	for groupName := range groups {
		if _, exists := groupNames[groupName]; exists {
			return fmt.Errorf("duplicate template group: %s", groupName)
		}
		groupNames[groupName] = true
	}
	return nil
}

// validateFileStructures checks for conflicts in file structures.
func validateFileStructures(groups map[string][]FileStructure) error {
	for _, files := range groups {
		for _, file := range files {
			if file.SourceFile != "" && file.Content != "" {
				return fmt.Errorf("both SourceFile and Content set for file: %s", file.Filename)
			}
			if file.SourceFile != "" {
				if _, err := os.Stat(file.SourceFile); os.IsNotExist(err) {
					return fmt.Errorf("template file not found: %s", file.SourceFile)
				}
			}
		}
	}
	return nil
}

// validateRepoGroupReferences checks if repositories refer to valid groups.
func validateRepoGroupReferences(repos []RepositoryConfig, groups map[string][]FileStructure) error {
	for _, repo := range repos {
		for _, groupRef := range repo.Groups {
			if _, exists := groups[groupRef.GroupName]; !exists {
				return fmt.Errorf("repository %s refers to non-existent group: %s", repo.Name, groupRef.GroupName)
			}
		}
	}
	return nil
}

// validateURLSchemes checks the validity of URLs in file structures.
func validateURLSchemes(groups map[string][]FileStructure) error {
	for _, files := range groups {
		for _, file := range files {
			if file.SourceURL != "" {
				if _, err := url.ParseRequestURI(file.SourceURL); err != nil {
					return fmt.Errorf("invalid SourceURL: %s", file.SourceURL)
				}
			}
		}
	}
	return nil
}

// deleteExistingDir deletes an existing directory and all its contents.
func deleteExistingDir(dirPath string) error {
	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		return nil
	}

	err := os.RemoveAll(dirPath)
	if err != nil {
		return fmt.Errorf("failed to delete existing directory '%s': %w", dirPath, err)
	}

	return nil
}

// processRepository processes a single repository configuration.
func processRepository(repo Repository, globalGroups map[string][]FileStructure, outputPath string) error {
	repoOutputPath := filepath.Join(outputPath, repo.Name)

	if err := deleteExistingDir(repoOutputPath); err != nil {
		return fmt.Errorf("error clearing output directory for repo %s: %w", repo.Name, err)
	}

	for _, file := range repo.Files {
		if err := createFileFromTemplate(repo.Name, file, outputPath); err != nil {
			return fmt.Errorf("error creating file from template: %w", err)
		}
	}

	for _, groupRef := range repo.Groups {
		group, exists := globalGroups[groupRef.GroupName]
		if !exists {
			return fmt.Errorf("template group %s not found in configuration", groupRef.GroupName)
		}

		template := Template{Files: make([]FileStructure, len(group))}
		for i, file := range group {
			template.Files[i] = file
			// Merge group-level values with file-level values
			mergedValues := mergeValues(groupRef.Values, file.Values)
			template.Files[i].Values = mergedValues
		}

		if err := generateFilesFromTemplate(repo.Name, template, outputPath); err != nil {
			return fmt.Errorf("error generating template for repo %s: %w", repo.Name, err)
		}
	}

	log.Printf("Repository '%s' processed successfully.\n", repo.Name)
	return nil
}

// mergeValues merges group-level values with file-level.
func mergeValues(groupValues, fileValues map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{})
	// Copy file-level values
	for k, v := range fileValues {
		merged[k] = v
	}
	// Overwrite with group-level values
	for k, v := range groupValues {
		merged[k] = v
	}
	return merged
}

// generateFilesFromTemplate generates files for a repository based on the provided template.
func generateFilesFromTemplate(repoName string, t Template, outputPath string) error {
	for _, file := range t.Files {
		if err := createFileFromTemplate(repoName, file, outputPath); err != nil {
			return fmt.Errorf("error creating file from template: %w", err)
		}
	}
	return nil
}

// createFileFromTemplate creates a file based on the FileStructure details.
func createFileFromTemplate(repoName string, file FileStructure, outputPath string) error {
	outputPath = filepath.Join(outputPath, repoName, filepath.Dir(file.Filename))
	if err := os.MkdirAll(outputPath, os.ModePerm); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	fullPath := filepath.Join(outputPath, filepath.Base(file.Filename))

	// Create file based on content, URL or file source.
	if file.Content != "" {
		return createTemplatedFile(fullPath, file.Content, file.Values)
	} else if file.SourceURL != "" {
		content, err := downloadFileContent(file.SourceURL)
		if err != nil {
			return fmt.Errorf("downloading file from URL: %w", err)
		}
		if err := createTemplatedFile(fullPath, content, file.Values); err != nil {
			return copyContentToFile(content, fullPath)
		}
	} else if file.SourceFile != "" {
		content, err := os.ReadFile(file.SourceFile)
		if err != nil {
			return fmt.Errorf("reading source file: %w", err)
		}
		if err := createTemplatedFile(fullPath, string(content), file.Values); err != nil {
			return copyFile(file.SourceFile, fullPath)
		}
	}
	return nil
}

// downloadFileContent fetches content from a URL and returns it as a string.
func downloadFileContent(fileURL string) (string, error) {
	resp, err := http.Get(fileURL)
	if err != nil {
		return "", fmt.Errorf("error making request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			fmt.Printf("error closing response body: %v\n", cerr)
		}
	}()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("bad response status: %d %s", resp.StatusCode, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response body: %w", err)
	}

	return string(body), nil
}

// createTemplatedFile creates a file from a template content and values.
func createTemplatedFile(path, content string, values map[string]interface{}) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating file: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			fmt.Printf("error closing file: %v\n", cerr)
		}
	}()

	tmpl, err := template.New(filepath.Base(path)).Parse(content)
	if err != nil {
		return fmt.Errorf("parsing template: %w", err)
	}

	if err := tmpl.Execute(f, values); err != nil {
		return err // Return the error to indicate templating failure
	}

	return nil
}

// copyFile copies a file from source to destination.
func copyFile(src, dst string) error {
	input, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("reading source file for copy: %w", err)
	}

	if err := os.WriteFile(dst, input, 0o644); err != nil {
		return fmt.Errorf("writing copied file: %w", err)
	}
	return nil
}

// copyContentToFile writes string content directly to a file.
func copyContentToFile(content, filePath string) error {
	return os.WriteFile(filePath, []byte(content), 0o644)
}