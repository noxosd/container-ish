package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"syscall"
)

type Token struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	IssuedAt    string `json:"issued_at"`
}

type Manifest struct {
	Digest   string `json:"digest"`
	Platform struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
	} `json:"platform"`
}

type Index struct {
	Manifests []Manifest `json:"manifests"`
}

type Layer struct {
	MediaType string `json:"mediaType"`
	Size      int    `json:"size"`
	Digest    string `json:"digest"`
}

type PlatfromManifest struct {
	Layers []Layer `json:"layers"`
}

// Ensures gofmt doesn't remove the imports above (feel free to remove this!)
var _ = os.Args
var _ = exec.Command

// Usage: your_docker.sh run <image> <command> <arg1> <arg2> ...
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <command> [<args>...]\n", os.Args[0])
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		run()
	case "child":
		child()
	case "download":
		download()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
	}
}

func download() {
	resp, err := http.Get("https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/hello-world:pull")
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	fmt.Printf("Response status: %s\n", resp.Status)
	// Further processing of the response can be done here
	t := &Token{}
	err = json.NewDecoder(resp.Body).Decode(t)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Got a token: %v\n", t.Token)

	client := &http.Client{}
	req, err := http.NewRequest("GET", "https://registry-1.docker.io/v2/library/hello-world/manifests/latest", nil)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Authorization", "Bearer "+t.Token)
	resp, err = client.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	fmt.Printf("Manifest response status: %s\n", resp.Status)
	// Further processing of the manifest can be done here
	fmt.Printf("Manifest response headers: %v\n", resp.Header)
	r, _ := io.ReadAll(resp.Body)
	// fmt.Printf("Resp body: %v\n", string(r))
	index := &Index{}
	err = json.Unmarshal(r, index)
	if err != nil {
		panic(err)
	}
	manifestDigest := ""
	for _, m := range index.Manifests {
		if m.Platform.Architecture == "amd64" && m.Platform.OS == "linux" {
			manifestDigest = m.Digest
			break
		}
	}

	manifestBytes, _, err := getBytes(client, "https://registry-1.docker.io/v2/library/hello-world/manifests/"+manifestDigest, *t)
	if err != nil {
		panic(err)
	}
	platfromManifest := PlatfromManifest{}
	err = json.Unmarshal(manifestBytes, &platfromManifest)
	if err != nil {
		panic(err)
	}

	layers := platfromManifest.Layers
	for _, layer := range layers {
		fmt.Printf("Layer: %v\n", layer)
		layerReader, resp, err := getStream(client, "https://registry-1.docker.io/v2/library/hello-world/blobs/"+layer.Digest, *t)
		if err != nil {
			panic(err)
		}

		fmt.Printf("Downloaded layer of size: %d\n", resp.ContentLength)
		uncomressedStream, err := gzip.NewReader(layerReader)
		if err != nil {
			panic(err)
		}
		tarReader := tar.NewReader(uncomressedStream)
		for {
			header, err := tarReader.Next()

			if err == io.EOF {
				break
			}

			if err != nil {
				log.Fatalf("ExtractTarGz: Next() failed: %s", err.Error())
			}

			dir := "./rootfs/"
			switch header.Typeflag {
			case tar.TypeDir:
				if err := os.Mkdir(dir+header.Name, 0755); err != nil {
					log.Fatalf("ExtractTarGz: Mkdir() failed: %s", err.Error())
				}
			case tar.TypeReg:
				outFile, err := os.Create(dir + header.Name)
				if err != nil {
					log.Fatalf("ExtractTarGz: Create() failed: %s", err.Error())
				}
				if _, err := io.Copy(outFile, tarReader); err != nil {
					log.Fatalf("ExtractTarGz: Copy() failed: %s", err.Error())
				}
				outFile.Close()

			default:
				log.Fatalf(
					"ExtractTarGz: uknown type: %s in %s",
					header.Typeflag,
					header.Name)
			}
		}

	}

}

// func parseDockerJSON[T any](t *T, b []byte) error {
// 	err := json.Unmarshal(b, t)
// 	if err != nil {
// 		return err
// 	}
// 	return nil
// }

func getBytes(client *http.Client, url string, t Token) ([]byte, http.Response, error) {
	respBody, resp, err := getStream(client, url, t)
	if err != nil {
		return nil, http.Response{}, err
	}
	r, err := io.ReadAll(respBody)
	if err != nil {
		return nil, http.Response{}, err
	}
	return r, resp, nil
}

func getStream(client *http.Client, url string, t Token) (io.Reader, http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, http.Response{}, err
	}
	req.Header.Set("Authorization", "Bearer "+t.Token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, http.Response{}, err
	}
	return resp.Body, *resp, nil
}
func run() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s run <image> <command> <arg1> <arg2> ...\n", os.Args[0])
	}
	command := os.Args[2]
	// args := os.Args[3:]

	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, command)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWPID | syscall.CLONE_NEWUTS,
	}

	must(cmd.Run())
}

func child() {
	fmt.Printf("Running %v as user %d in process %d\n", os.Args[2:], os.Getuid(), os.Getpid())
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s run <image> <command> <arg1> <arg2> ...\n", os.Args[0])
	}
	command := os.Args[2]
	args := os.Args[3:]

	cmd := exec.Command(command, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	must(syscall.Sethostname([]byte("container")))
	must(syscall.Chroot("/home/nox/Projects/own-docker/container-ish/rootfs/blobs/sha256/"))
	must(syscall.Chdir("/"))
	must(os.MkdirAll("/proc", 0555))
	must(syscall.Mount("proc", "/proc", "proc", 0, ""))

	must(syscall.Exec("./hello", append([]string{command}, args...), os.Environ()))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
