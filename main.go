package main

import (
	"fmt"
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
	Config struct {
		MediaType string `json:"mediaType"`
		Size      int    `json:"size"`
		Digest    string `json:"digest"`
	} `json:"config"`
	Layers []Layer `json:"layers"`
}

type Config struct {
	Config struct {
		CMD []string `json:"Cmd"`
		Env []string `json:"Env"`
	} `json:"config"`
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
		command := os.Args[2]
		args := os.Args[3:]
		run(command, args, os.Environ())
	case "child":
		command := os.Args[2]
		args := os.Args[3:]
		child(command, args)
	case "download":
		image := os.Args[2]
		err := download(image)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error downloading image: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
	}
}

func download(image string) error {
	t, err := getToken(image)
	if err != nil {
		return fmt.Errorf("got error getting a token: %w", err)
	}

	platfromManifest, err := getManifest(image, *t)
	if err != nil {
		return fmt.Errorf("got error getting manifest: %w", err)
	}

	config, err := getConfig(image, platfromManifest, *t)
	if err != nil {
		return fmt.Errorf("got error getting config: %w", err)
	}

	fmt.Printf("Downloading layers\n")
	err = getLayers(image, platfromManifest, *t)
	if err != nil {
		return fmt.Errorf("got error getting layers: %w", err)
	}

	cmd := config.Config.CMD[0]
	args := config.Config.CMD[1:]
	env := config.Config.Env
	fmt.Printf("Downloaded image %s with command: %s %v\n", image, cmd, args)

	run(cmd, args, env)
	return nil
}

// func parseDockerJSON[T any](t *T, b []byte) error {
// 	err := json.Unmarshal(b, t)
// 	if err != nil {
// 		return err
// 	}
// 	return nil
// }

func run(command string, args []string, env []string) {

	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, command)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWPID | syscall.CLONE_NEWUTS,
	}

	must(cmd.Run())
}

func child(command string, args []string) {
	fmt.Printf("Running %v as user %d in process %d\n", os.Args[2:], os.Getuid(), os.Getpid())
	fmt.Printf("Command: %s, Args: %v\n", command, args)
	// cmd := exec.Command("bin/sh", args...)
	// cmd.Stdin = os.Stdin
	// cmd.Stdout = os.Stdout
	// cmd.Stderr = os.Stderr
	// cmd.Env = []string{"PATH=/bin"}

	must(syscall.Sethostname([]byte("container")))
	must(syscall.Chroot("rootfs"))
	must(syscall.Chdir("/"))
	must(os.MkdirAll("/proc", 0555))
	must(syscall.Mount("proc", "/proc", "proc", 0, ""))

	// fmt.Println("Env:", cmd.Env)
	commandPath, err := exec.LookPath(command)
	if err != nil {
		panic(err)
	}

	command = commandPath
	must(os.Chmod(command, 0755))

	defer syscall.Unmount("/proc", 0)

	must(syscall.Exec(command, append([]string{command}, args...), os.Environ()))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
