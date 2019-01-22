set -x

bazel clean
bazel build --action_env=GOPATH=$GOPATH --action_env=PATH=$PATH //release:v2ray_darwin_amd64_package
workingdir=`pwd`
cd bazel-bin/release && unzip -o v2ray-macos.zip
chmod +x v2ray v2ctl
cd $workingdir
