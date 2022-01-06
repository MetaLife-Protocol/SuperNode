#!/bin/sh
export VERSION=1.0.0

#build for linux/amd64 because of meshbox must need cgo
# ./xgobuild.sh 
# zip -r photon_linux_amd64_$VERSION.zip photon-$VERSION-linux-amd64
# rm -f photon-$VERSION-linux-amd64


#linux/arm
export CGO_ENABLED=0
export GOOS=linux
export GOARCH=arm
export GOARM=5
go env
./build.sh
mv supernode supernode_linux_arm_$VERSION
zip -r supernode_linux_arm_$VERSION.zip supernode_linux_arm_$VERSION
rm -f supernode_linux_arm_$VERSION
#linux版本要考虑到meshbox的需要,所以必须支持cgo
export GOOS=linux
export GOARCH=amd64
./build.sh
mv supernode supernode_linux_amd64_$VERSION
zip -r supernode_linux_amd64_$VERSION.zip supernode_linux_amd64_$VERSION
rm -f supernode_linux_amd64_$VERSION

#windows
export GOOS=windows
export GOARCH=amd64
./build.sh
mv supernode.exe supernode_windows_amd64_$VERSION.exe
zip -r supernode_windows_amd64_$VERSION.zip supernode_windows_amd64_$VERSION.exe
rm -f supernode_windows_amd64_$VERSION.exe

#darwin
export GOOS=darwin
export GOARCH=amd64
./build.sh
mv supernode supernode_darwin_amd64_$VERSION
zip -r supernode_darwin_amd64_$VERSION.zip supernode_darwin_amd64_$VERSION
rm -f supernode_darwin_amd64_$VERSION

