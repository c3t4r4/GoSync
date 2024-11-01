# GoSync
App to check and copy files between two folders

## Instalando as Dependencias
```sh
go mod tidy
```

### Caso precise Atualizar as dependencias
```sh
go get -u
```

## Compilando no Unix para Windows
```sh
GOOS=windows GOARCH=amd64 go build -o sync.exe sync.go
```

## Compilando
```sh
go build -o sync.exe sync.go
```