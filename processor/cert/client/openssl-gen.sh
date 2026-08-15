#!/bin/bash
set -ex
# generate CA's  key
openssl genrsa -aes256 -passout Asd123456~ -out ca.key.pem 4096

openssl req -config openssl.cnf -key ca.key.pem -new -x509 -days 7300 -sha256 -extensions v3_ca -out ca.pem
