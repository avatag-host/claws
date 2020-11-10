amd64:
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -gcflags "all=-trimpath=$(pwd)" -o build/claws_linux_amd64 -v claws.go

arm64:
	GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -gcflags "all=-trimpath=$(pwd)" -o build/claws_linux_arm64 -v claws.go

compress:
	upx --brute build/claws_*

cross-build: clean build compress

clean:
	rm -rf build/claws_*

.PHONY: all build compress clean