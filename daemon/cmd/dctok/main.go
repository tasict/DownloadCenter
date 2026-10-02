// dctok is a test helper: it lists users and creates or revokes a personal
// access token directly in a package database. Not shipped.
package main

import (
	"fmt"
	"os"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/store"
)

func main() {
	db, err := store.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	a := auth.New(db)
	switch os.Args[2] {
	case "users":
		for _, u := range a.Users() {
			fmt.Println(u.Name, u.Role, u.QTSAdmin)
		}
	case "create":
		t := &auth.Token{Owner: os.Args[3], Name: "test", Scopes: auth.AllScopes, Tasks: "all"}
		v, err := a.CreateToken(t, true)
		if err != nil {
			panic(err)
		}
		fmt.Println(t.ID, v)
	case "revoke":
		a.RevokeToken(os.Args[3], os.Args[4])
	}
}
