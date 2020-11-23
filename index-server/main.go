package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"path"
	"path/filepath"
	"time"

	"github.com/deislabs/oras/pkg/content"
	"github.com/deislabs/oras/pkg/oras"
	indexSchema "github.com/devfile/registry-support/index/generator/schema"

	"github.com/containerd/containerd/remotes/docker"
	"github.com/gin-gonic/gin"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	devfileName            = "devfile.yaml"
	devfileConfigMediaType = "application/vnd.devfileio.devfile.config.v2+json"
	devfileMediaType       = "application/vnd.devfileio.devfile.layer.v1"
	registryPath           = "/registry/stacks"
	indexPath              = "/registry/index.json"
	scheme                 = "http"
	registryService        = "localhost:5000"
)

func main() {
	// Wait until registry is up and running
	isDone := false
	for !isDone {
		resp, err := http.Get(scheme + "://" + registryService)
		if err != nil {
			log.Fatal(err.Error())
		}

		if resp.StatusCode == http.StatusOK {
			isDone = true
			log.Println("Registry is up and running")
		}
		log.Println("Waiting for registry to start...")
		time.Sleep(time.Second)
	}

	// Load index file
	bytes, err := ioutil.ReadFile(indexPath)
	if err != nil {
		log.Fatalf("failed to read index file: %v", err)
	}

	var index []indexSchema.Schema
	err = json.Unmarshal(bytes, &index)
	if err != nil {
		log.Fatalf("failed to unmarshal index file: %v", err)
	}

	// Before starting the server, push the devfile artifacts to the registry
	for _, devfileIndex := range index {
		err := pushStackToRegistry(devfileIndex)
		if err != nil {
			log.Fatal(err.Error())
		}
	}

	// Start the server and serve requests and index.json
	router := gin.Default()

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "the server is up and running",
		})
	})

	router.GET("/devfiles/:name", func(c *gin.Context) {
		name := c.Param("name")
		for _, devfileIndex := range index {
			if devfileIndex.Name == name {
				bytes, err := pullStackFromRegistry(devfileIndex)
				if err != nil {
					log.Fatal(err.Error())
					c.JSON(http.StatusInternalServerError, gin.H{
						"error":  err.Error(),
						"status": fmt.Sprintf("failed to pull the devfile of %s", name),
					})
				}
				c.Data(http.StatusOK, http.DetectContentType(bytes), bytes)
			}
		}
	})

	router.StaticFile("/index", indexPath)

	router.Run(":7070")
}

// pushStackToRegistry pushes the given devfile stack to the OCI registry
func pushStackToRegistry(devfileIndex indexSchema.Schema) error {
	// Load the devfile into memory and set up the pushing resource (file name, file content, media type, ref)
	devfileContent, err := ioutil.ReadFile(filepath.Join(registryPath, devfileIndex.Name, devfileName))
	if err != nil {
		return err
	}
	ref := path.Join(registryService, "/", devfileIndex.Links["self"])

	ctx := context.Background()
	resolver := docker.NewResolver(docker.ResolverOptions{PlainHTTP: true})

	// Add the devfile (and its custom media type) to the memory store
	memoryStore := content.NewMemoryStore()
	desc := memoryStore.Add(devfileName, devfileMediaType, devfileContent)
	pushContents := []ocispec.Descriptor{desc}

	log.Printf("Pushing %s to %s...\n", devfileName, ref)
	desc, err = oras.Push(ctx, resolver, ref, memoryStore, pushContents, oras.WithConfigMediaType(devfileConfigMediaType))
	if err != nil {
		return fmt.Errorf("failed to push %s to %s: %v", devfileName, ref, err)
	}
	log.Printf("Pushed to %s with digest %s\n", ref, desc.Digest)
	return nil
}

// pullStackFromRegistry pulls the given devfile stack from the OCI registry
func pullStackFromRegistry(devfileIndex indexSchema.Schema) ([]byte, error) {
	// Pull the devfile from registry and save to disk
	ref := path.Join(registryService, "/", devfileIndex.Links["self"])

	ctx := context.Background()
	resolver := docker.NewResolver(docker.ResolverOptions{PlainHTTP: true})

	// Initialize memory store
	memoryStore := content.NewMemoryStore()
	allowedMediaTypes := []string{devfileMediaType}

	log.Printf("Pulling %s from %s...\n", devfileName, ref)
	desc, _, err := oras.Pull(ctx, resolver, ref, memoryStore, oras.WithAllowedMediaTypes(allowedMediaTypes))
	if err != nil {
		return nil, fmt.Errorf("failed to pull %s from %s: %v", devfileName, ref, err)
	}
	_, bytes, ok := memoryStore.GetByName(devfileName)
	if !ok {
		return nil, fmt.Errorf("failed to load %s to memory", devfileName)
	}

	log.Printf("Pulled from %s with digest %s\n", ref, desc.Digest)
	return bytes, nil
}
