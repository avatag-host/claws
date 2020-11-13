windows-amd64:
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -gcflags "all=-trimpath=$(pwd)" -o build/claws_windows_amd64 -v claws.go

linux-arm64:
	GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -gcflags "all=-trimpath=$(pwd)" -o build/claws_linux_arm64 -v claws.go

linux-amd64:
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -gcflags "all=-trimpath=$(pwd)" -o build/claws_linux_amd64 -v claws.go

clean:
	rm -rf build/claws_*
