package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func getToken(image string) (*Token, error) {
	url := "https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/" + image + ":pull"
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// fmt.Printf("Response status: %s\n", resp.Status)
	// Further processing of the response can be done here
	t := &Token{}
	err = json.NewDecoder(resp.Body).Decode(t)
	if err != nil {
		return nil, err
	}
	// fmt.Printf("Got a token: %v\n", t.Token)
	return t, nil
}

func getManifest(image string, t Token) (*PlatfromManifest, error) {
	baseURL := "https://registry-1.docker.io/v2/library/" + image
	client := &http.Client{}

	manifestIndex, err := getBytes(client, baseURL+"/manifests/latest", t)
	if err != nil {
		return nil, fmt.Errorf("error getting manifest index: %w", err)
	}
	// fmt.Println(string(manifestIndex))
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
		// fmt.Println(string(manifestBytes))
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
		return nil, fmt.Errorf("got error getting config: %w", err)
	}

	fmt.Println(string(configBytes))
	configStruct := &Config{}
	err = json.Unmarshal(configBytes, configStruct)
	if err != nil {
		return nil, fmt.Errorf("got error unmarshaling config: %w", err)
	}
	return configStruct, nil
}

func getLayers(image string, manifest *PlatfromManifest, t Token) error {
	baseURL := "https://registry-1.docker.io/v2/library/" + image
	client := &http.Client{}
	layers := manifest.Layers

	// ROOTFS := "rootfs/"
	LAYERS_DIR := "layers/"
	tmpDir, err := os.MkdirTemp(os.TempDir(), "containerish-layers-")
	if err != nil {
		return fmt.Errorf("got error creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	var wg sync.WaitGroup

	for _, layer := range layers {
		layerDir := filepath.Join(LAYERS_DIR, strings.ReplaceAll(layer.Digest, ":", "-"))
		exists, err := layerExists(layerDir)
		if err != nil {
			return fmt.Errorf("got error checking if layer exists: %w", err)
		}
		if exists {
			fmt.Println("Layer already exists, skipping downloading:", layerDir)
			continue
		}

		wg.Add(1)
		go func(l Layer) {
			defer wg.Done()
			fmt.Printf("Layer: %v\n", l)
			resp, err := getResponse(client, baseURL+"/blobs/"+l.Digest, t)
			if err != nil {
				panic(err)
			}
			fmt.Printf("Downloaded layer of size: %d\n", resp.ContentLength)
			layerReader := resp.Body
			defer layerReader.Close()

			tmpFile := filepath.Join(tmpDir, l.Digest+".tar.gz")
			out, err := os.Create(tmpFile)
			if err != nil {
				panic(err)
			}
			_, err = io.Copy(out, layerReader)
			if err != nil {
				panic(err)
			}
		}(layer)
	}
	wg.Wait()

	fmt.Println("All layers downloades, startiing extraction...")
	for _, layer := range layers {
		layerDir := filepath.Join(LAYERS_DIR, strings.ReplaceAll(layer.Digest, ":", "-"))
		exists, err := layerExists(layerDir)
		if err != nil {
			return fmt.Errorf("got error checking if layer exists: %w", err)
		}
		if exists {
			fmt.Println("Layer already exists, skipping extracting:", layerDir)
			continue
		}
		wg.Add(1)
		go func(l Layer) {
			defer wg.Done()
			err := os.MkdirAll(layerDir, 0755)
			if err != nil {
				panic(err)
			}
			layerFile := filepath.Join(tmpDir, l.Digest+".tar.gz")
			layerReader, err := os.Open(layerFile)
			if err != nil {
				panic(err)
			}
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

				fp := filepath.Join(layerDir, header.Name)
				// if strings.Contains(header.Name, ".wh.") {
				// 	fmt.Printf("We've got a whiteout file: %s\n", header.Name)
				// 	whFile := strings.ReplaceAll(header.Name, ".wh.", "")
				// 	whPath := filepath.Join(ROOTFS, whFile)
				// 	err := os.RemoveAll(whPath)
				// 	if err != nil {
				// 		log.Fatalf("Failed to remove whiteout target: %s, %s", whPath, err.Error())
				// 	}
				// 	fmt.Printf("Removed whiteout target: %s\n", whPath)
				// 	continue
				// }

				switch header.Typeflag {
				case tar.TypeDir:
					if err := os.Mkdir(fp, 0755); err != nil {
						if !errors.Is(err, os.ErrExist) {
							log.Fatalf("ExtractTarGz: Mkdir() failed: %s", err.Error())
						}
					}
				case tar.TypeReg:
					outFile, err := os.Create(fp)
					if err != nil {
						log.Fatalf("ExtractTarGz: Create() failed: %s", err.Error())
					}
					os.Chmod(fp, header.FileInfo().Mode().Perm())
					if _, err := io.Copy(outFile, tarReader); err != nil {
						log.Fatalf("ExtractTarGz: Copy() failed: %s", err.Error())
					}
					outFile.Close()
				case tar.TypeLink:
					linkTarget := filepath.Join(layerDir, header.Linkname)
					if err := os.Link(linkTarget, fp); err != nil {
						log.Fatalf("ExtractTarGz: Link() failed: %s", err.Error())
					}
				case tar.TypeSymlink:
					// fmt.Printf("Creating symlink %s -> %s\n", header.Name, header.Linkname)
					if err := os.Symlink(header.Linkname, fp); err != nil {
						log.Fatalf("ExtractTarGz: Symlink() failed: %s", err.Error())
					}
				default:
					log.Fatalf(
						"ExtractTarGz: uknown type: %s in %s",
						header.Typeflag,
						header.Name)
				}
			}
		}(layer)
	}

	wg.Wait()
	return nil
}

func buildRootFS(manifest *PlatfromManifest) error {
	// ROOTFS := "rootfs/"
	LAYERS_DIR := "layers/"

	for _, layer := range manifest.Layers {
		layerDir := filepath.Join(LAYERS_DIR, strings.ReplaceAll(layer.Digest, ":", "-"))
		err := dirCopy(layerDir, "rootfs/")
		if err != nil {
			return fmt.Errorf("got error copying layer to rootfs: %w", err)
		}

	}
	return nil
}

func dirCopy(src string, dst string) error {
	WalkDirFunc := func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if path == src {
			return nil
		}

		relPath := strings.TrimPrefix(path, src)
		destPath := filepath.Join(dst, relPath)
		// fmt.Printf("Will copy %s to %s\n", path, destPath)

		if strings.Contains(path, ".wh.") {
			fmt.Printf("GOT a whiteout - %s", path)
			whFile := strings.ReplaceAll(destPath, ".wh.", "")
			err := os.RemoveAll(whFile)
			if err != nil {
				return err
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		if d.IsDir() {
			if err := os.MkdirAll(destPath, info.Mode().Perm()); err != nil {
				return err
			}
			return nil
		}

		if d.Type()&fs.ModeSymlink != 0 {
			if _, err := os.Lstat(destPath); err == nil {
				// Link already exists
				return nil
			}

			target, _ := os.Readlink(path)
			return os.Symlink(target, destPath)
		}

		srcF, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcF.Close()

		dstF, err := os.Create(destPath)
		if err != nil {
			return err
		}

		if _, err = io.Copy(dstF, srcF); err != nil {
			return err
		}

		dstF.Close()
		if strings.Contains(destPath, "redis-server") {
			fmt.Printf("Permissions - %v\n", info.Mode().Perm())
		}
		return os.Chmod(destPath, info.Mode().Perm())
	}

	err := filepath.WalkDir(src, WalkDirFunc)
	if err != nil {
		log.Fatalf("got an error copying the dir: %v", err)
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

func layerExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err

}
