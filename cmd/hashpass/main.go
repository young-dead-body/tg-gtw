package main
import ("fmt";"os";"github.com/company/telegram-gateway/api/internal/password")
func main(){h,err:=password.New().Hash(os.Args[1]);if err!=nil{panic(err)};fmt.Print(h)}
