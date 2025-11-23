package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
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
		CMD        []string `json:"Cmd"`
		Env        []string `json:"Env"`
		Entrypoint []string `json:"Entrypoint"`
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

func cleanup() {
	err := syscall.Unmount("rootfs/proc", 0)
	if err != nil {
		fmt.Printf("Error unmounting /proc: %v\n", err)
	}
	err = os.RemoveAll("rootfs")
	if err != nil {
		fmt.Printf("Error cleaning up rootfs: %v\n", err)
	}

	err = deleteNftableTable()
	if err != nil {
		fmt.Printf("Error deleting nftables table: %v\n", err)
	}

}

func download(image string) error {

	err := os.MkdirAll("rootfs", 0755)
	if err != nil {
		return fmt.Errorf("got error creating rootfs dir: %w", err)
	}

	err = os.MkdirAll("layers", 0755)
	if err != nil {
		if !os.IsExist(err) {
			return fmt.Errorf("got error creating layers dir: %w", err)
		}
	}

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

	cmd := ""
	var args []string
	env := config.Config.Env
	entrypoint := config.Config.Entrypoint
	if len(entrypoint) > 0 {
		cmd = entrypoint[0]
		args = config.Config.CMD
		fmt.Println("ARGS:", args)
	} else {
		cmd = config.Config.CMD[0]
		args = config.Config.CMD[1:]
	}
	fmt.Printf("Downloaded image %s with command: %s %v\n", image, cmd, args)

	buildRootFS(platfromManifest)
	run(cmd, args, env)
	cleanup()
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
	fmt.Println("Running command in a new container:", command, args)
	fullCommand := append([]string{command}, args...)
	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, fullCommand...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWPID | syscall.CLONE_NEWUTS | syscall.CLONE_NEWNET | syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		Setsid:     true,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Geteuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
		GidMappingsEnableSetgroups: false,
	}
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt)
	go func() {
		s := <-sigc
		cmd.Process.Signal(s)
	}()

	fmt.Println("============STARRTING CONTAINER============")
	err := cmd.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running command: %v\n", err)
		os.Exit(-1)
	}

	pid := cmd.Process.Pid
	err = addNetworkInterfaces(pid)
	if err != nil {
		fmt.Printf("Error adding network interfaces: %v\n", err)
		os.Exit(-1)
	}

	err = cmd.Wait()
	if err != nil {
		fmt.Printf("Command executed unsucsessfully: %s\n", err)
		os.Exit(-1)
	}
	fmt.Printf("Container PID: %d\n", pid)
	fmt.Println("Container stopped")
}

func child(command string, args []string) {
	fmt.Printf("Running %v as user %d in process %d\n", os.Args[2:], os.Getuid(), os.Getpid())
	must(syscall.Sethostname([]byte("container")))
	must(syscall.Chroot("rootfs"))
	must(syscall.Chdir("/"))
	must(os.MkdirAll("/proc", 0555))
	must(syscall.Mount("proc", "/proc", "proc", 0, ""))
	defer syscall.Unmount("/proc", 0)

	fmt.Println("Env:", os.Environ())
	commandPath, err := exec.LookPath(command)
	if err != nil {
		panic(err)
	}

	command = commandPath
	// must(os.Chmod(command, 0777))
	fmt.Printf("Command: %s, Args: %v\n", command, args)
	must(syscall.Exec(command, append([]string{command}, args...), os.Environ()))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
