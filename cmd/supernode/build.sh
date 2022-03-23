#!/bin/sh
export CGO_ENABLED=1
source ../../env.sh 
echo $GIT_COMMIT

go  build  -ldflags "   -X github.com/MetaLife-Protocol/SuperNode/cmd/supernode/mainimpl.GitCommit=$GIT_COMMIT -X github.com/MetaLife-Protocol/SuperNode/cmd/supernode/mainimpl.GoVersion=$GO_VERSION -X github.com/MetaLife-Protocol/SuperNode/cmd/supernode/mainimpl.BuildDate=$BUILD_DATE -X github.com/MetaLife-Protocol/SuperNode/cmd/supernode/mainimpl.Version=$VERSION "

cp supernode $GOPATH/bin
