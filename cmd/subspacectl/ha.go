package main

import (
	"net/http"
)

var haCommands = table{
	"status": plain("ha status", request{method: http.MethodGet, path: "ha"}),
}
