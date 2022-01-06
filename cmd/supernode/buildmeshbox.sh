#!/bin/sh
# must enable cgo,because of plugin
source ../../env.sh 
export CGO_ENABLED=1
echo $GIT_COMMIT
go  build  -ldflags "   -X github.com/MetaLife-Protocol/SuperNode/cmd/photon/mainimpl.GitCommit=$GIT_COMMIT -X github.com/MetaLife-Protocol/SuperNode/cmd/photon/mainimpl.GoVersion=$GO_VERSION -X github.com/MetaLife-Protocol/SuperNode/cmd/photon/mainimpl.BuildDate=$BUILD_DATE -X github.com/MetaLife-Protocol/SuperNode/cmd/photon/mainimpl.Version=$VERSION "
cp photon $GOPATH/bin
