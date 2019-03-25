sudo rm -rf /tmp/runsc
bazel build runsc
sudo cp ./bazel-bin/runsc/linux_amd64_pure_stripped/runsc /usr/local/bin
