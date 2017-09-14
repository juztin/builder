package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/dustin/go-humanize"
	"github.com/jhoonb/archivex"
)

type authConfig struct {
	types.AuthConfig
}

// dockerClient wraps a Docker client and stores an encoded auth string for use with registry calls.
type dockerClient struct {
	*client.Client
	AuthConfig authConfig
}

// dockerStream is used to unmarshal messages from the Docker API.
type dockerStream struct {
	Stream string `json: "stream"`
}

// fileInfo object that includes the path of the file.
type fileInfo struct {
	os.FileInfo
	Path string
}

// stat holds statistics for an image build.
type stat struct {
	Id            string
	Tags          []string
	DockerFile    string
	Architecture  string
	Os, OsVersion string
	Size          int64
	Build, Push   time.Duration
}

// Value returns the base64 encoded auth string.
func (c authConfig) Value() (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// Write pushes the formatted stats information to the supplied writer.
func (s stat) Write(w io.Writer) error {
	size := humanize.Bytes(uint64(s.Size))
	msg := fmt.Sprintf("Dockerfile: %s\n"+
		"        Id: %s\n"+
		"      Tags: %s\n"+
		"   Arch/OS: %s/%s %s\n"+
		"      Size: %s\n"+
		"Build Time: %s\n"+
		" Push Time: %s\n", s.DockerFile, s.Id, strings.Join(s.Tags, ", "), s.Architecture, s.Os, s.OsVersion, size, s.Build, s.Push)
	_, err := w.Write([]byte(msg))
	return err
}

// build Builds a Docker image using the given client and dockerFile, tagging the resulting image with the supplied tags.
func (c *dockerClient) build(dockerFile string, tags []string) (types.ImageBuildResponse, string, error) {
	options := types.ImageBuildOptions{
		PullParent:     true,
		NoCache:        true,
		SuppressOutput: false,
		Tags:           tags,
		Remove:         true,
		ForceRemove:    true,
	}

	ctx, err := createContext(dockerFile)
	if err != nil {
		return types.ImageBuildResponse{}, "", err
	}
	defer ctx.Close()
	resp, err := c.ImageBuild(context.Background(), ctx, options)
	return resp, ctx.Name(), err
}

//push pushes the the image to the registry.
func (c *dockerClient) push(image string) (io.ReadCloser, error) {
	auth, err := c.AuthConfig.Value()
	if err != nil {
		return nil, err
	}
	options := types.ImagePushOptions{RegistryAuth: auth}
	return c.ImagePush(context.Background(), image, options)
}

// authConfig returns an encoded authorization string.
func newAuthConfig(username, password, email, auth, registry string) authConfig {
	cfg := types.AuthConfig{
		Auth:          auth,
		Username:      username,
		Password:      password,
		Email:         email,
		ServerAddress: registry,
	}
	return authConfig{cfg}
}

// newClient returns a new Docker client.
func newClient(version string, a authConfig) (*dockerClient, error) {
	client, err := client.NewClient("unix:///var/run/docker.sock", version, nil, nil)
	if err != nil {
		return nil, err
	}

	return &dockerClient{client, a}, nil
}

// tagsFor returns a list of names to tag the resulting image as.
//
//    """
//    #!/bin/bash
//
//    git diff --name-only $(git rev-parse HEAD^) $(git rev-parse HEAD) | { grep "Dockerfile" || true; } | paste -s -d, -
//    """
//
func tagsFor(dockerFile string) ([]string, error) {
	file, err := os.Open(dockerFile)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	tags := []string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "#" {
			if len(tags) == 0 {
				continue
			} else {
				break
			}
		}

		tag := strings.TrimSpace(line[1:])
		tags = append(tags, tag)
	}

	err = scanner.Err()
	if err == nil && len(tags) == 0 {
		err = fmt.Errorf("Failed to find any tags within: %s", dockerFile)
	}
	return tags, err
}

// filesIn finds all files, recursively, within the given path.
func filesIn(path string) ([]fileInfo, error) {
	files := []fileInfo{}
	err := filepath.Walk(path, func(path string, f os.FileInfo, err error) error {
		files = append(files, fileInfo{f, path})
		return nil
	})
	return files, err
}

// createContext Creates the build context for Docker (recursively tars all files for the path where dockerFile resides).
func createContext(dockerFile string) (*os.File, error) {
	path := filepath.Dir(dockerFile)
	tempFile := filepath.Join(os.TempDir(), "docker_context.tar.gz")
	tar := new(archivex.TarFile)
	tar.Create(tempFile)
	tar.AddAll(path, false)
	tar.Close()
	return os.Open(tempFile)
}

// dockerFiles returns the given files as their fully qualified path.
func dockerFiles(files []string) ([]string, error) {
	s := []string{}
	for _, f := range files {
		path, err := filepath.Abs(f)
		if err != nil {
			return s, err
		}
		s = append(s, path)
	}
	return s, nil
}

// readln parses all JSON messages for an invocation to the Docker API.
func readln(r *bufio.Reader) (string, error) {
	var (
		isPrefix bool  = true
		err      error = nil
		line, ln []byte
		j        dockerStream
	)

	for isPrefix && err == nil {
		line, isPrefix, err = r.ReadLine()
		ln = append(ln, line...)
	}
	if err == nil {
		err = json.Unmarshal(ln, &j)
	}
	return j.Stream, err
}

// writeResponse buffers responses from the Docker API to stdout.
func writeResponse(w io.Writer, r io.ReadCloser) ([]string, error) {
	//defer r.Close()
	ids := []string{}
	b := bufio.NewReader(r)
	s, err := readln(b)
	for err == nil {
		// Attempt to get all image ids during build.
		if strings.HasPrefix(s, " ---> ") {
			id := strings.TrimSpace(s[len(" ---> "):])
			if len(id) == 12 { // Skip non-image ids (eg. "Running in a430b8c0596e")
				ids = append(ids, id)
			}
		}
		fmt.Fprint(w, s)
		s, err = readln(b)
	}

	if err == io.EOF {
		err = nil
		r.Close()
	}
	return ids, err
}

// arguments returns the authentication configuration, version, and Docker files, from the supplied command line arguments.
func arguments() (cfg authConfig, version string, fileNames []string, cleanup bool) {
	username := flag.String("username", "", "Docker registry username")
	password := flag.String("password", "", "Docker registry password")
	email := flag.String("email", "", "Docker registered email")
	auth := flag.String("auth", "", "Docker registry auth")
	ver := flag.String("version", "1.28", "Docker registry version") // just kinda randomly picked this default version.
	clean := flag.Bool("cleanup", true, "Removes all created images")
	registry := flag.String("registry", "", "Docker registry server (required)")
	files := flag.String("files", "", "List of Dockerfiles to build, separated by comma (required)")
	flag.Parse()

	// Enforce that both `files` and `registry` values were supplied.
	if *files == "" || *registry == "" {
		flag.PrintDefaults()
		os.Exit(1)
	}

	// If any credential value was supplied, then all of them must be supplied.
	if strings.TrimSpace(*username+*password+*email) != "" {
		if *username == "" || *password == "" || *email == "" {
			flag.PrintDefaults()
			fmt.Println("Username, password, and email are required together")
			os.Exit(1)
		}
	}

	// Create new client with authentication.
	cfg = newAuthConfig(*username, *password, *email, *auth, *registry)
	fileNames = strings.Split(*files, ",")
	version = *ver
	cleanup = *clean
	return
}

// checkErr outputs the error and message to stdout and exist if err is not nil.
func checkErr(err error, msg string) {
	if err != nil {
		fmt.Printf("\n***** ERROR ***** \n%s\n%s\n", msg, err)
		os.Exit(1)
	}
}

func main() {
	start := time.Now()

	// Create client
	authCfg, version, fileNames, cleanup := arguments()
	docker, err := newClient(version, authCfg)
	checkErr(err, "Failed to create Docker client")

	// Find all Docker files
	files, err := dockerFiles(fileNames)
	checkErr(err, "Failed to get valid Docker files")

	// Display list of files to be processed
	fmt.Println("\n#################### Processing:")
	fmt.Printf("\t%s\n", strings.Join(files, "\n\t"))

	// Build each Dockerfile
	stats := []stat{}
	for _, file := range files {
		// Stats
		var ids []string
		s := &stat{DockerFile: file, Size: -1}

		// --- Process Dockerfile
		fmt.Printf("\n########## Tags: %s\n", file)
		tags, err := tagsFor(file)
		checkErr(err, fmt.Sprintf("Failed to get retrieve tags %s", file))
		s.Tags = tags
		for i := range tags {
			fmt.Printf("\tTag: %s\n", tags[i])
		}

		// --- Build image
		fmt.Printf("\n########## Building: %s\n", file)
		t := time.Now()
		// Stage the build
		resp, filename, err := docker.build(file, tags)
		checkErr(err, fmt.Sprintf("Failed to stage build %s", file))

		// Process stream from API.
		ids, err = writeResponse(os.Stdout, resp.Body)
		checkErr(err, fmt.Sprintf("Failed to build %s", file))
		s.Build = time.Since(t)
		s.Id = ids[len(ids)-1]

		// --- Delete build context
		os.Remove(filename)

		// --- Push image/tags
		fmt.Printf("\n########## Pushing: %s\n", file)
		t = time.Now()
		for _, tag := range tags {
			fmt.Printf("\tTag: %s\n", tag)
			r, err := docker.push(tag)
			if err == nil {
				_, err = writeResponse(os.Stdout, r)
			}
			checkErr(err, fmt.Sprintf("Failed to push tag %s", tag))
		}
		s.Push = time.Since(t)

		// Get image size
		image, _, err := docker.ImageInspectWithRaw(context.Background(), s.Id)
		if err == nil {
			s.Size = image.Size
			s.Architecture = image.Architecture
			s.Os = image.Os
			s.OsVersion = image.OsVersion
		}
		stats = append(stats, *s)

		if cleanup {
			// --- Cleanup
			fmt.Printf("\n########## Removing:\n")
			// Delete backwards through the created images (decendant images first)
			for i := len(ids) - 1; i > 0; i-- {
				fmt.Printf("\t%s\n", ids[i])
				_, err = docker.ImageRemove(context.Background(), ids[i], types.ImageRemoveOptions{Force: true})
				if err != nil {
					fmt.Println("Failed to remove image:", ids[i])
				}
			}
		}
	}
	fmt.Println("\n#################### Success:")
	for i := range stats {
		stats[i].Write(os.Stdout)
		fmt.Println("")
	}
	fmt.Println("Finished in:", time.Since(start))
}
