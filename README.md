# SuperNode

## Photon Installation

### Photon system environment requirements

Golang version 1.12.3 or higher
The available running memory needs to reach 2G and above, and the available disk space needs to reach 50G and above.

### Photon Install

```go
go get github.com/SmartMeshFoundation/Photon/
cd Photon/cmd/photon
go install
```

### Start Photon

Before starting Photon, you need to create an account on Spectrum, for example **"0x97cd7291f93f9582ddb8e9885bf7e77e3f34be40"**. You also need to set the keystore path and password file or password in the boot script.
```go
photon \
--datadir=.photon \
--api-address=0.0.0.0:15001 \
--listen-address=127.0.0.1:15003\ 
--address="0x...{your node address}" \
--keystore-path {your keystore path} \
--password-file  {your keystore password file}\
--eth-rpc-endpoint ws://transport01.smartmesh.cn:33333   \
--debug  \
--verbosity 5  \
--logfile ./log \
--registry-contract-address 0x242e0de2B118279D1479545A131a90A8f67A2512 \
--xmpp \
--pfs http://transport01.smartmesh.cn:7000
 
```

**Parameter Description:**

- address is the binding address of this photon node
- keystore-path includes the keystore file path with address
- password-file is used to decrypt the password file of address
- logfile   log storage path
 
It should be noted that this address is the PhotonAddress address in the metalife pub monitoring service
