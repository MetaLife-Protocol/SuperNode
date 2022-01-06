# SuperNode
![](http://img.shields.io/travis/SmartMeshFoundation/Photon.svg)
![](https://github.com/dognie/Photon/blob/master/docs/photon.png?raw=true)


 [SuperNode documentation](https://SuperNodeNetwork.readthedocs.io/en/latest/)

 SuperNode is an off-chain scaling solution for MetaLife.
## Project Status
  This project is still very much a work in progress. It can be used for testing, but it should not be used for real funds. We are doing our best to identify and fix problems, and implement missing features. Any help testing the implementation, reporting bugs, or helping with outstanding issues is very welcome.

## Build
```
  go get github.com/MetaLife-Protocol/SuperNode/
  cd $GOPATH/github.com/MetaLife-Protocol/SuperNode
  make 
  ./build/bin/supernode
```

## mobile support
SuperNode can works on Android and iOS using mobile's API.  it needs [go mobile](https://github.com/golang/mobile) to build mobile library.
### build Android mobile library
```bash
cd mobile
./build_Android.sh 
```
then you can integrate `mobile.aar` into your project.
### build iOS mobile framework
```bash
./build_iOS.sh
```
then you can integrate `Mobile.framework` into your project.
## Requirements
Latest version of SMC

We need go's plugin module.
