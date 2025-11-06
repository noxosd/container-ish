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
)

func getToken(image string) (*Token, error) {
	url := "https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/" + image + ":pull"
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	fmt.Printf("Response status: %s\n", resp.Status)
	// Further processing of the response can be done here
	t := &Token{}
	err = json.NewDecoder(resp.Body).Decode(t)
	if err != nil {
		return nil, err
	}
	fmt.Printf("Got a token: %v\n", t.Token)
	return t, nil
}

func getManifest(image string, t Token) (*PlatfromManifest, error) {
	baseURL := "https://registry-1.docker.io/v2/library/" + image
	client := &http.Client{}

	manifestIndex, err := getBytes(client, baseURL+"/manifests/latest", t)
	if err != nil {
		return nil, fmt.Errorf("error getting manifest index: %w", err)
	}
	fmt.Println(string(manifestIndex))
	index := &Index{}
	err = json.Unmarshal(manifestIndex, index)
	if err != nil {
		return nil, fmt.Errorf("error unmarshaling manifest index: %w", err)
	}
	manifestDigest := ""
	for _, m := range index.Manifests {
		if m.Platform.Architecture == "amd64" && m.Platform.OS == "linux" {
			manifestDigest = m.Digest
			break
		}
	}

	manifestBytes, err := getBytes(client, baseURL+"/manifests/"+manifestDigest, t)
	if err != nil {
		return nil, fmt.Errorf("error getting platform manifest: %w", err)
	}
	platfromManifest := PlatfromManifest{}
	err = json.Unmarshal(manifestBytes, &platfromManifest)
	if err != nil {
		fmt.Println(string(manifestBytes))
		return nil, fmt.Errorf("error unmarshaling platform manifest: %w", err)
	}

	return &platfromManifest, nil
}

func getConfig(image string, manifest *PlatfromManifest, t Token) (*Config, error) {
	baseURL := "https://registry-1.docker.io/v2/library/" + image
	client := &http.Client{}
	config := manifest.Config

	configBytes, err := getBytes(client, baseURL+"/blobs/"+config.Digest, t)
	if err != nil {
		return nil, fmt.Errorf("Got error getting config: %w", err)
	}

	configStruct := &Config{}
	err = json.Unmarshal(configBytes, configStruct)
	if err != nil {
		return nil, fmt.Errorf("Got error unmarshaling config: %w", err)
	}
	return configStruct, nil
}

func getLayers(image string, manifest *PlatfromManifest, t Token) error {
	baseURL := "https://registry-1.docker.io/v2/library/" + image
	client := &http.Client{}
	layers := manifest.Layers
	for _, layer := range layers {
		fmt.Printf("Layer: %v\n", layer)
		resp, err := getResponse(client, baseURL+"/blobs/"+layer.Digest, t)
		if err != nil {
			panic(err)
		}

		fmt.Printf("Downloaded layer of size: %d\n", resp.ContentLength)
		layerReader := resp.Body
		defer layerReader.Close()
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
	return nil
}

func getBytes(client *http.Client, url string, t Token) ([]byte, error) {
	resp, err := getResponse(client, url, t)
	if err != nil {
		return nil, err
	}
	r, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func getResponse(client *http.Client, url string, t Token) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t.Token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}
