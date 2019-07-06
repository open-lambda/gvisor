# Container FS Benchmark Tests

## Build
```
gcc -o test <test c source code file>
```
e.g.
```
gcc -o test test_read.c
```

### Run
1. Generate temp test files: `./test new`. It generates test files for each layer.
2. Copy them and test program to image build folder.
3. Run `./test dockerfile` to generate Dockerfile code.
4. Modify your current Dockerfile and add the generated code to the bottom of your Dockerfile.
5. Use `docker build -t <image name> .` to build your Dockerfile.
6. If you need to test on container FS, please take appropriate steps to convert the docker image to container FS image.
7. Start the container. In container, Run `cd /root; ./test <options>`. Options are:
    * `test`: only run the test once
    * `test_repeat`: run the test multiple times. By default it is 30 but you can run `./test test_repeat 10` to overwrite the setting and use 10 as repeat times.
    * `test_rev`: run the test once but with reversed accessing order: access the deepest layer first.
    * `test_repeat_rev`: Almost same as `test_repeat` but in reversed order

