# Introduction

This is the codebase for Container FS project, which is a new layered file system for gvisor.

## Branches
1. **master**: master branch is the original code forked from gvisor official repo
2. **imgfs**: imgfs branch only contains the code that supports user to mount one specified image file from local disk to a hardcoded mount point `/img`.
3. **imgfs\_exp**: imgfs\_exp branch uses container fs to replace the old root fs backed by goferfs. Container fs contains different imgfs layers which represents different layers in original docker iamge.

# Environment

## Install Bazel (version 0.23, latest version may not work)

```
wget https://github.com/bazelbuild/bazel/releases/download/0.23.0/bazel-0.23.0-installer-linux-x86_64.sh
chmod +x bazel-0.23.0-installer-linux-x86_64.sh
./bazel-0.23.0-installer-linux-x86_64.sh --user
# Example for setting correct path for fish.
# Please change it if you are using bash/zsh etc.
echo "set -gx PATH \$PATH $HOME/bin" >> ~/.config/fish/config.fish
```

## Install Golang

```
wget https://dl.google.com/go/go1.12.5.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.12.5.linux-amd64.tar.gz
```

## Install Docker

```
sudo apt update
sudo apt-get remove docker docker-engine docker.io containerd runc
sudo apt-get install -y \
    apt-transport-https \
    ca-certificates \
    curl \
    gnupg-agent \
    software-properties-common
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo apt-key add -
sudo apt-key fingerprint 0EBFCD88
sudo add-apt-repository \
   "deb [arch=amd64] https://download.docker.com/linux/ubuntu \
   $(lsb_release -cs) \
   stable"
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io
sudo groupadd docker
sudo usermod -aG docker $USER
```
# Run gVisor

## Config Docker (KVM platform)

Create `/etc/docker/daemon.json` and write down the following JSON data:
```
{
    "runtimes": {
        "runsc": {
            "path": "/usr/local/bin/runsc",
            "runtimeArgs": [
                "--platform=kvm",
            ]
       }
    }
}
```

**You should restart docker to make sure that the new config takes into effect.**

## Compile

1. Use bazel to compile runsc: `bazel build runsc`
2. Copy the generated binary to `/usr/local/bin`: `sudo cp ./bazel-bin/runsc/linux_amd64_pure_stripped/runsc /usr/local/bin`

## Start Container

Runtime flag should be added so docker can know which runtime to use. e.g. `docker run --runtime=runsc -it test_image /bin/bash`

## Debug

If you need debug feature, your `/etc/docker/daemon.json` should contain debug flags. e.g.
```
{
    "runtimes": {
        "runsc": {
            "path": "/usr/local/bin/runsc",
            "runtimeArgs": [
                "--debug-log=/tmp/runsc/",
                "--debug",
                "--platform=kvm",
            ]
       }
    }
}
```
**You should restart docker to make sure that the new config takes into effect.** After that the debug log will be generated in the specific folder. In the provided example the location will be /tmp/runsc.

# Code Structure

## File System

Different FS implementations are located in `pkg/sentry/fs`. Container FS is constructed by many layers of Image FS, which was developed in an earlier stage of this project. All Image FS implementation are located in `pkg/sentry/fs/imgfs`

The first step to mount a file system is `Mount` method in `pkg/sentry/fs/imgfs/fs.go`. Not only it reads from the specified image and contructs the directory tree based on the metadata stored in the image, but also returns an initial root inode, which is used to mount to the mount point.

More details about inode operation is defined in `pkg/sentry/fs/imgfs/inode.go`. (e.g. `MapInternal`, `AddMapping`, `RemoveMapping` are used for mmap support, some parts of methods will return syserror.EPERM because we assume imgfs should be read-only.)

Container FS actually is not a "filesystem" here but can be considered as procedures that piling up different layers of ImgFS images. It is completed in the booting stage (`runsc/boot/fs.go`). `MountExpFS` is the main function for constructing container FS.

# Create ContainerFS Image
Container FS requires a special docker image which only contains different ImgFS images representing different layers. **Docker images downloaded directly from Docker Hub can't work without special processing.** Detailed information of creating ConatinerFS image will be mentioned in the README of [zar project](https://github.com/open-lambda/zar).

