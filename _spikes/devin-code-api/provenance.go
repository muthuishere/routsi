package main

import "os"

type provenance struct {
	BinaryReadable bool     `json:"binary_readable"`
	Connect        bool     `json:"connect_protocol"`
	Methods        []string `json:"methods"`
	SchemaSource   string   `json:"schema_source"`
}

var knownMethods = []string{
	"/exa.api_server_pb.ApiServerService/GetCliModelConfigs",
	"/exa.api_server_pb.ApiServerService/AssignModel",
	"/exa.api_server_pb.ApiServerService/GetChatMessage",
}

func inspectBinary(path string) provenance {
	p := provenance{SchemaSource: "markers embedded in installed Devin CLI; no standalone FileDescriptorSet found"}
	b, err := os.ReadFile(path)
	if err != nil {
		return p
	}
	p.BinaryReadable = true
	p.Connect = contains(b, []byte("connect-protocol-version")) && contains(b, []byte("application/proto"))
	for _, m := range knownMethods {
		if contains(b, []byte(m)) {
			p.Methods = append(p.Methods, m)
		}
	}
	return p
}

func contains(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
