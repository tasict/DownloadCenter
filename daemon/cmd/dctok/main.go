// dctok is a test helper: it lists who may use Download Center and creates
// or revokes a personal access token directly in a package database. Not
// shipped.
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
		for _, u := range a.Members().Members {
			fmt.Println(u.Name, u.Admin)
		}
	case "create":
		// What the owner may do, like a token made in the UI
		admin := auth.IsAdmin(os.Args[3])
		scopes := []string{}
		for _, sc := range auth.AllScopes {
			if admin || !auth.AdminScopes[sc] {
				scopes = append(scopes, sc)
			}
		}
		t := &auth.Token{Owner: os.Args[3], Name: "test", Scopes: scopes, Tasks: "all"}
		v, err := a.CreateToken(t, admin)
		if err != nil {
			panic(err)
		}
		fmt.Println(t.ID, v)
	case "revoke":
		a.RevokeToken(os.Args[3], os.Args[4])
	}
}
