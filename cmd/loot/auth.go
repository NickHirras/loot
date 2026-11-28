package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const authUsage = `Usage:
  loot auth hash  [--cost 12]   Hash a password with bcrypt for auth.password_hash
  loot auth token [--prefix …]  Generate a random API token for api.read_tokens / write_tokens

Examples:
  echo -n 'my-secret-password' | loot auth hash > /etc/loot/auth-password-hash.txt
  loot auth token >> /etc/loot/api-read-tokens.txt
`

func runAuth(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(authUsage)
		return nil
	}
	sub := args[0]
	args = args[1:]

	switch sub {
	case "hash":
		return runAuthHash(args)
	case "token":
		return runAuthToken(args)
	default:
		return fmt.Errorf("unknown auth command %q\n\n%s", sub, authUsage)
	}
}

func runAuthHash(args []string) error {
	fs := flag.NewFlagSet("auth hash", flag.ExitOnError)
	cost := fs.Int("cost", 12, "bcrypt cost factor (10-14)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cost < bcrypt.MinCost || *cost > bcrypt.MaxCost {
		return fmt.Errorf("cost must be between %d and %d", bcrypt.MinCost, bcrypt.MaxCost)
	}

	fi, err := os.Stdin.Stat()
	if err != nil {
		return err
	}
	var password string
	if (fi.Mode() & os.ModeCharDevice) != 0 {
		fmt.Fprint(os.Stderr, "Password: ")
		line, rerr := bufio.NewReader(os.Stdin).ReadString('\n')
		if rerr != nil && rerr != io.EOF {
			return rerr
		}
		password = strings.TrimRight(line, "\r\n")
	} else {
		data, rerr := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if rerr != nil {
			return rerr
		}
		password = strings.TrimRight(string(data), "\r\n")
	}
	if password == "" {
		return fmt.Errorf("password must not be empty")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), *cost)
	if err != nil {
		return err
	}
	fmt.Println(string(hash))
	return nil
}

func runAuthToken(args []string) error {
	fs := flag.NewFlagSet("auth token", flag.ExitOnError)
	prefix := fs.String("prefix", "loot_", "token prefix")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return err
	}
	fmt.Println(*prefix + base64.RawURLEncoding.EncodeToString(buf[:]))
	return nil
}
