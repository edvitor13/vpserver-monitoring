package web

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Usuários do painel, em <data>/users.json (0600). A senha fica só como hash
// PBKDF2. O primeiro administrador nasce do .env (VPMON_USER/VPMON_PASSWORD)
// e, enquanto não trocar a senha pela tela, entra com a do .env (Hash vazio).
// O auth.json antigo (um login só, de antes dos usuários) é migrado sozinho.

type User struct {
	Name       string `json:"name"`
	Hash       string `json:"hash,omitempty"` // vazio = senha do .env (só o administrador inicial)
	Admin      bool   `json:"admin,omitempty"`
	Actions    bool   `json:"actions,omitempty"` // pausar/retomar aplicações
	Manage     bool   `json:"manage,omitempty"`  // criar e gerenciar usuários
	MustChange bool   `json:"mustChange,omitempty"`
	Created    int64  `json:"created,omitempty"`
	CreatedBy  string `json:"createdBy,omitempty"`
	Changed    int64  `json:"changed,omitempty"` // última troca de senha pela tela
	LastLogin  int64  `json:"lastLogin,omitempty"`
	Epoch      string `json:"epoch,omitempty"` // muda junto com senha/permissões: derruba as sessões da pessoa

	TOTP     *TOTPState `json:"totp,omitempty"`     // verificação em duas etapas (nil = desligada)
	Asked2FA bool       `json:"asked2fa,omitempty"` // a recomendação de ligar o 2FA já foi feita
}

func (u User) CanAct() bool    { return u.Admin || u.Actions }
func (u User) CanManage() bool { return u.Admin || u.Manage }

// Perms são as permissões que dá para conceder.
type Perms struct {
	Admin   bool `json:"admin"`
	Actions bool `json:"actions"`
	Manage  bool `json:"manage"`
}

// UserView é o que a tela vê de um usuário (sem hash nem epoch).
type UserView struct {
	Name       string `json:"name"`
	Admin      bool   `json:"admin"`
	Actions    bool   `json:"actions"`
	Manage     bool   `json:"manage"`
	MustChange bool   `json:"mustChange"`
	Created    int64  `json:"created"`
	CreatedBy  string `json:"createdBy,omitempty"`
	LastLogin  int64  `json:"lastLogin"`
	EnvPass    bool   `json:"envPassword,omitempty"` // entra com a senha do .env
	TwoFA      bool   `json:"twoFA"`
}

func (u User) View() UserView {
	return UserView{Name: u.Name, Admin: u.Admin, Actions: u.CanAct(), Manage: u.CanManage(), MustChange: u.MustChange,
		Created: u.Created, CreatedBy: u.CreatedBy, LastLogin: u.LastLogin, EnvPass: u.Hash == "", TwoFA: u.TOTP != nil}
}

type usersFile struct {
	Users []User `json:"users"`
}

var (
	ErrBadCurrent = errors.New("a senha atual não confere")
	ErrWeak       = errors.New("a nova senha precisa ter pelo menos 10 caracteres")
	ErrSame       = errors.New("a nova senha é igual à atual")
	ErrBadUser    = errors.New("usuário inválido: use de 3 a 32 letras, números, ponto, hífen ou _")
	ErrNoStore    = errors.New("o painel está sem pasta de dados")
	ErrUserExists = errors.New("já existe um usuário com esse nome")
	ErrNoUser     = errors.New("usuário não encontrado")
	ErrForbidden  = errors.New("você não tem permissão para isso")
	ErrSelf       = errors.New("isso não vale para o seu próprio usuário (use Minha conta)")
	ErrLastAdmin  = errors.New("tem de sobrar pelo menos um administrador")
)

var validUser = regexp.MustCompile(`^[A-Za-z0-9._-]{3,32}$`)

func randomEpoch() string {
	b := make([]byte, 9)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// tempPassword gera a senha provisória (xxxx-xxxx-xxxx, sem letras parecidas).
func tempPassword() string {
	const chars = "abcdefghijkmnpqrstuvwxyz23456789"
	var b strings.Builder
	for i := 0; i < 12; i++ {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b.WriteByte(chars[n.Int64()])
	}
	return b.String()
}

// --- arquivo -----------------------------------------------------------------------------

func readUsers(path string) ([]User, time.Time, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	var f usersFile
	if json.Unmarshal(b, &f) != nil || len(f.Users) == 0 {
		return nil, time.Time{}, false
	}
	return f.Users, st.ModTime(), true
}

func writeUsers(path string, users []User) (time.Time, error) {
	b, _ := json.MarshalIndent(usersFile{Users: users}, "", "  ")
	tmp := filepath.Join(filepath.Dir(path), ".users.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return time.Time{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return time.Time{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}, nil
	}
	return st.ModTime(), nil
}

// initialUsers monta a lista quando ainda não há users.json: migra o
// auth.json antigo ou cria o administrador do .env (só em memória).
func initialUsers(dir, envUser string, forceChange bool) (users []User, migrated bool) {
	if dir != "" {
		if s, ok := loadStored(filepath.Join(dir, "auth.json")); ok {
			name := s.User
			if name == "" {
				name = envUser
			}
			return []User{{Name: name, Hash: s.Hash, Changed: s.Changed, Admin: true}}, true
		}
	}
	return []User{{Name: envUser, Admin: true, MustChange: forceChange}}, false
}

// --- consultas ---------------------------------------------------------------------------

func findUser(users []User, name string) int {
	for i, u := range users {
		if u.Name == name {
			return i
		}
	}
	return -1
}

func nameTaken(users []User, name, except string) bool {
	for _, u := range users {
		if strings.EqualFold(u.Name, name) && u.Name != except {
			return true
		}
	}
	return false
}

func countAdmins(users []User) int {
	n := 0
	for _, u := range users {
		if u.Admin {
			n++
		}
	}
	return n
}

// Get devolve um usuário pelo nome.
func (a *Auth) Get(name string) (User, bool) {
	a.reload()
	a.mu.RLock()
	defer a.mu.RUnlock()
	if i := findUser(a.users, name); i >= 0 {
		return a.users[i], true
	}
	return User{}, false
}

// Users lista os usuários (administradores primeiro, depois por nome).
func (a *Auth) Users() []UserView {
	a.reload()
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]UserView, 0, len(a.users))
	for _, u := range a.users {
		out = append(out, u.View())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Admin != out[j].Admin {
			return out[i].Admin
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// --- mudanças ------------------------------------------------------------------------------

// mutate aplica uma mudança e grava. f roda com a lista travada e pode recusar.
func (a *Auth) mutate(f func(users []User) ([]User, error)) error {
	if a.dir == "" {
		return ErrNoStore
	}
	a.reload()
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := append([]User(nil), a.users...)
	next, err := f(cp)
	if err != nil {
		return err
	}
	mt, err := writeUsers(a.path, next)
	if err != nil {
		return err
	}
	a.users, a.mtime = next, mt
	return nil
}

// ChangePassword troca a própria senha (confere a atual) e, se vier, o nome.
// Devolve o usuário atualizado; as sessões dele caem (a chave muda com o hash).
func (a *Auth) ChangePassword(name, current, next, newName string) (User, error) {
	u, ok := a.Get(name)
	if !ok {
		return User{}, ErrNoUser
	}
	if !a.checkPass(u, current) {
		return User{}, ErrBadCurrent
	}
	if len([]rune(next)) < 10 {
		return User{}, ErrWeak
	}
	if next == current {
		return User{}, ErrSame
	}
	newName = strings.TrimSpace(newName)
	if newName == "" {
		newName = u.Name
	}
	if !validUser.MatchString(newName) {
		return User{}, ErrBadUser
	}
	hash := HashPassword(next)
	var out User
	err := a.mutate(func(users []User) ([]User, error) {
		i := findUser(users, name)
		if i < 0 {
			return nil, ErrNoUser
		}
		if nameTaken(users, newName, name) {
			return nil, ErrUserExists
		}
		for j := range users { // quem foi criado por ela continua apontando para ela
			if users[j].CreatedBy == name {
				users[j].CreatedBy = newName
			}
		}
		users[i].Name, users[i].Hash, users[i].Changed, users[i].MustChange = newName, hash, time.Now().Unix(), false
		out = users[i]
		return users, nil
	})
	return out, err
}

// CreateUser cria um usuário com senha provisória (devolvida uma vez só).
// Quem não é administrador cria só não-administradores, com no máximo as
// próprias permissões.
func (a *Auth) CreateUser(actor User, name string, p Perms) (UserView, string, error) {
	if !actor.CanManage() {
		return UserView{}, "", ErrForbidden
	}
	name = strings.TrimSpace(name)
	if !validUser.MatchString(name) {
		return UserView{}, "", ErrBadUser
	}
	if err := allowedPerms(actor, p); err != nil {
		return UserView{}, "", err
	}
	pass := tempPassword()
	hash := HashPassword(pass)
	var out User
	err := a.mutate(func(users []User) ([]User, error) {
		if nameTaken(users, name, "") {
			return nil, ErrUserExists
		}
		out = User{Name: name, Hash: hash, Admin: p.Admin, Actions: p.Actions, Manage: p.Manage, MustChange: true,
			Created: time.Now().Unix(), CreatedBy: actor.Name, Epoch: randomEpoch()}
		return append(users, out), nil
	})
	return out.View(), pass, err
}

func allowedPerms(actor User, p Perms) error {
	if actor.Admin {
		return nil
	}
	if p.Admin || (p.Actions && !actor.CanAct()) || (p.Manage && !actor.CanManage()) {
		return fmt.Errorf("%w: só dá para conceder as permissões que você tem", ErrForbidden)
	}
	return nil
}

// target confere se actor pode mexer em name (não em si mesmo; não-admin não mexe em admin).
func target(users []User, actor User, name string) (int, error) {
	if !actor.CanManage() {
		return -1, ErrForbidden
	}
	if name == actor.Name {
		return -1, ErrSelf
	}
	i := findUser(users, name)
	if i < 0 {
		return -1, ErrNoUser
	}
	if users[i].Admin && !actor.Admin {
		return -1, fmt.Errorf("%w: só administradores mexem em administradores", ErrForbidden)
	}
	return i, nil
}

// UpdateUser muda as permissões de alguém (as sessões dessa pessoa caem).
func (a *Auth) UpdateUser(actor User, name string, p Perms) (UserView, error) {
	if err := allowedPerms(actor, p); err != nil {
		return UserView{}, err
	}
	var out User
	err := a.mutate(func(users []User) ([]User, error) {
		i, err := target(users, actor, name)
		if err != nil {
			return nil, err
		}
		if users[i].Admin && !p.Admin && countAdmins(users) <= 1 {
			return nil, ErrLastAdmin
		}
		users[i].Admin, users[i].Actions, users[i].Manage, users[i].Epoch = p.Admin, p.Actions, p.Manage, randomEpoch()
		out = users[i]
		return users, nil
	})
	return out.View(), err
}

// ResetUser gera uma senha provisória nova para alguém (troca obrigatória no próximo acesso).
func (a *Auth) ResetUser(actor User, name string) (string, error) {
	pass := tempPassword()
	hash := HashPassword(pass)
	err := a.mutate(func(users []User) ([]User, error) {
		i, err := target(users, actor, name)
		if err != nil {
			return nil, err
		}
		users[i].Hash, users[i].MustChange, users[i].Epoch = hash, true, randomEpoch()
		return users, nil
	})
	return pass, err
}

// DeleteUser remove alguém (nunca a si mesmo nem o último administrador).
func (a *Auth) DeleteUser(actor User, name string) error {
	return a.mutate(func(users []User) ([]User, error) {
		i, err := target(users, actor, name)
		if err != nil {
			return nil, err
		}
		if users[i].Admin && countAdmins(users) <= 1 {
			return nil, ErrLastAdmin
		}
		return append(users[:i], users[i+1:]...), nil
	})
}

// MarkLogin anota o último acesso (melhor esforço).
func (a *Auth) MarkLogin(name string) {
	a.mutate(func(users []User) ([]User, error) {
		if i := findUser(users, name); i >= 0 {
			users[i].LastLogin = time.Now().Unix()
		}
		return users, nil
	})
}

// ResetPasswordOffline é o `vpmon reset-password <usuário>`: troca a senha de
// alguém por uma provisória direto no users.json, com o painel rodando (ele
// percebe a mudança no arquivo), e desliga o 2FA dessa pessoa (quem esqueceu
// a senha pode ter perdido o celular também). Sem users.json, parte do admin do .env.
func ResetPasswordOffline(dir, envUser, name string) (string, error) {
	path := filepath.Join(dir, "users.json")
	users, _, ok := readUsers(path)
	if !ok {
		users, _ = initialUsers(dir, envUser, false)
	}
	i := findUser(users, name)
	if i < 0 {
		var names []string
		for _, u := range users {
			names = append(names, u.Name)
		}
		return "", fmt.Errorf("não existe o usuário %q (há: %s)", name, strings.Join(names, ", "))
	}
	pass := tempPassword()
	users[i].Hash, users[i].MustChange, users[i].Epoch, users[i].TOTP = HashPassword(pass), true, randomEpoch(), nil
	if _, err := writeUsers(path, users); err != nil {
		return "", err
	}
	return pass, nil
}
