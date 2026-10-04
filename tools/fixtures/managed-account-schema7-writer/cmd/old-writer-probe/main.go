package main
import ("errors";"fmt";"os";"strconv";"github.com/mhsanaei/3x-ui/v3/internal/policyauthority")
func main(){if len(os.Args)!=4{os.Exit(4)};generation,err:=strconv.ParseUint(os.Args[3],10,64);if err!=nil{os.Exit(4)};j,err:=policyauthority.Open(os.Args[1],policyauthority.Identity{AuthorityID:os.Args[2],Generation:generation});if errors.Is(err,policyauthority.ErrJournal){fmt.Println("REJECTED_JOURNAL");os.Exit(3)};if err!=nil{fmt.Println("OTHER_ERROR");os.Exit(4)};if err:=j.Close();err!=nil{os.Exit(4)};fmt.Println("OPENED")}
