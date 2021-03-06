package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Person : stores a single person's data
type Person struct {
	Name   string
	Number int
}

// People :
type people struct {
	People []Person
}

// SessionData : to send back session data
type SessionData struct {
	Auth        bool
	HasPassword bool
	Google      bool
}

type passwordChangeData struct {
	OldPassword string
	NewPassword string
}

// Response : used for returning status data to user
type Response struct {
	Success bool
	Msg     string
}

type oauthProvider struct {
	IS          string `json:"id" bson:"id"`
	AccessToken string `json:"accessToken" bson:"accessToken"`
}

type user struct {
	ID       primitive.ObjectID `json:"id" bson:"_id"`
	Email    string             `json:"email" bson:"email"`
	Password string             `json:"password" bson:"password"`
	Google   *oauthProvider     `json:"Google" bson:"Google,omitempty"`
	// other oauthProviders
	Profile *profile `json:"profile" bson:"profile,omitempty"`
}

type oauthUserData struct {
	Email       string
	ID          string
	Picture     string
	Name        string
	AccessToken string
}

type profile struct {
	Name    string
	Picture string
}

type otc struct {
	Code string
}

var store *sessions.CookieStore
var googleOauthConfig *oauth2.Config
var googleRandomState = RandStringRunes(30)

const oauthGoogleURLAPI = "https://www.googleapis.com/oauth2/v2/userinfo?access_token="

func init() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	googleOauthConfig = &oauth2.Config{
		RedirectURL:  os.Getenv("GOOGLE_REDIRECT_URL"),
		ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		Scopes:       []string{"https://www.googleapis.com/auth/userinfo.email", "https://www.googleapis.com/auth/userinfo.profile"},
		Endpoint:     google.Endpoint,
	}

	store = sessions.NewCookieStore([]byte(os.Getenv("SESSION_SECRET1")),
		[]byte(os.Getenv("SESSION_SECRET2")))

	store.Options = &sessions.Options{
		MaxAge:   3600 * 8, // 8 hours
		HttpOnly: true,
	}
}

func main() {

	// setting up database
	DBSetup()

	// clearing cache
	ctx := context.Background()
	DB.Collection("cache").DeleteMany(ctx, bson.M{})

	router := mux.NewRouter()
	router.HandleFunc("/", func(res http.ResponseWriter, req *http.Request) {
		fmt.Fprint(res, "This is the index page.")
	})

	// api routes
	router.HandleFunc("/api/session", fetchSession).Methods("GET")
	router.Handle("/api/people", onlyAuthorized(http.HandlerFunc(fetchPeople))).Methods("GET")
	router.Handle("/api/auth", onlyUnauthorized(http.HandlerFunc(authorize))).Methods("POST")
	router.Handle("/api/register", onlyUnauthorized(http.HandlerFunc(register))).Methods("POST")
	router.Handle("/api/changePassword", onlyAuthorized(http.HandlerFunc(changePassword))).Methods("POST")
	router.Handle("/api/logout", onlyAuthorized(http.HandlerFunc(logout))).Methods("POST")

	// oauth management
	router.HandleFunc("/api/authOTC", oauthLink).Methods("POST")
	router.Handle("/api/google", onlyAuthorized(http.HandlerFunc(oauthGoogleUnlink))).Methods("DELETE")

	// oauth linking
	router.HandleFunc("/auth/google", oauthGoogleRedirect).Methods("GET")
	router.HandleFunc("/callback/google", oauthGoogleCallback).Methods("GET")

	log.Println("Listening on port " + os.Getenv("PORT"))
	http.ListenAndServe(":"+os.Getenv("PORT"), router)
}

func logout(res http.ResponseWriter, req *http.Request) {

	// deleting session
	session, _ := store.Get(req, "boiler-session")
	session.Options.MaxAge = -1
	err := session.Save(req, res)
	if err != nil {
		log.Fatal("failed to delete session", err)
	}

	res.WriteHeader(http.StatusOK)
}

func register(res http.ResponseWriter, req *http.Request) {
	// decoding userdata
	decoder := json.NewDecoder(req.Body)
	var postedUserData user
	err := decoder.Decode(&postedUserData)
	if err != nil {
		log.Panicln(err)
	}

	log.Printf("Registering user: %s", postedUserData.Email)

	res.Header().Set("Content-Type", "application/json")

	// checking for duplicates
	ctx := context.Background()
	foundUser := DB.Collection("users").FindOne(ctx, bson.M{"email": postedUserData.Email})
	var dupe user
	decodeError := foundUser.Decode(&dupe)
	if decodeError == nil {
		log.Println("Registration failed. Duplicate user.")
		response, _ := json.Marshal(Response{false, "An user with that email already exists!"})
		res.WriteHeader(http.StatusBadRequest)
		res.Write(response)
		return
	}

	// hashing password
	hashed, _ := bcrypt.GenerateFromPassword([]byte(postedUserData.Password), 12)
	hashedConverted := string(hashed)

	// inserting user
	creationResult, creationError := DB.Collection("users").InsertOne(ctx, bson.M{"email": postedUserData.Email, "password": hashedConverted})
	log.Println(creationResult)
	if creationError != nil {
		log.Panicln(creationError)
	}

	log.Println("Registration successful.")

	// setting session data
	session, _ := store.Get(req, "boiler-session")
	session.Values["auth"] = true // now able to get users in the index page
	session.Values["id"] = creationResult.InsertedID.(primitive.ObjectID).Hex()
	session.Values["hasPassword"] = true
	session.Values["Google"] = false

	if err = sessions.Save(req, res); err != nil {
		log.Printf("Error saving session: %v", err)
	}

	// sending a success response
	response, err := json.Marshal(Response{true, "Successfully registered!"})
	if err != nil {
		log.Println("Could not marshal response")
	}
	res.Write(response)
}

func authorize(res http.ResponseWriter, req *http.Request) {
	// decoding userdata
	decoder := json.NewDecoder(req.Body)
	var postedUserData user
	err := decoder.Decode(&postedUserData)
	if err != nil {
		log.Panicln(err)
	}

	log.Printf("Logging in: %s", postedUserData.Email)
	res.Header().Set("Content-Type", "application/json")

	// fetching user
	ctx := context.Background()
	foundUser := DB.Collection("users").FindOne(ctx, bson.M{"email": postedUserData.Email})
	var u user
	decodeError := foundUser.Decode(&u)
	if decodeError != nil {
		log.Println(decodeError)
		log.Println("Login failed. No user.")
		response, _ := json.Marshal(Response{false, "Invalid login details!"})
		res.WriteHeader(http.StatusUnauthorized)
		res.Write(response)
		return
	}

	// checking password
	comparisonError := bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(postedUserData.Password))
	if comparisonError != nil {
		log.Println("Login failed. Wrong password.")
		response, _ := json.Marshal(Response{false, "Invalid login details!"})
		res.WriteHeader(http.StatusUnauthorized)
		res.Write(response)
		return
	}

	// setting session data
	session, _ := store.Get(req, "boiler-session")
	session.Values["auth"] = true // now able to get users in the index page
	session.Values["id"] = u.ID.Hex()

	if u.Password != "" {
		session.Values["hasPassword"] = true
	} else {
		session.Values["hasPassword"] = false
	}

	if u.Google != nil {
		session.Values["Google"] = true
	} else {
		session.Values["Google"] = false
	}

	if err = sessions.Save(req, res); err != nil {
		log.Printf("Error saving session: %v", err)
	}

	// sending a success response
	response, err := json.Marshal(Response{true, "Successfully logged in!"})
	if err != nil {
		log.Println("Could not marshal response")
	}
	res.Write(response)
}

func fetchPeople(res http.ResponseWriter, req *http.Request) {
	// send back mock people
	session, _ := store.Get(req, "boiler-session")
	authStatus, ok := session.Values["auth"].(bool)
	if !ok || !authStatus {
		log.Println("Couldnt cast session auth to bool. Unauthorized.")
		res.WriteHeader(http.StatusForbidden)
		return
	}

	ppl := people{[]Person{{"Jack Hill", 421}, {"Jack Wright", 212}}}

	js, err := json.Marshal(ppl)
	if err != nil {
		http.Error(res, err.Error(), http.StatusInternalServerError)
		return
	}

	res.Header().Set("Content-Type", "application/json")
	res.Write(js)
}

func fetchSession(res http.ResponseWriter, req *http.Request) {
	// send back the session data
	session, _ := store.Get(req, "boiler-session")
	authStatus, ok := session.Values["auth"].(bool)
	if !ok {
		authStatus = false
	}

	// checking for nil values
	if session.Values["hasPassword"] == nil {
		session.Values["hasPassword"] = false
	}

	if session.Values["Google"] == nil {
		session.Values["Google"] = false
	}

	sessionData := SessionData{authStatus, session.Values["hasPassword"].(bool), session.Values["Google"].(bool)}
	js, err := json.Marshal(sessionData)
	if err != nil {
		http.Error(res, err.Error(), http.StatusInternalServerError)
		return
	}

	res.Header().Set("Content-Type", "application/json")
	res.Write(js)
}

func oauthGoogleRedirect(res http.ResponseWriter, req *http.Request) {

	keys, ok := req.URL.Query()["redirectUrl"]

	if !ok || len(keys[0]) < 1 {
		log.Println("Redirection URL is missing.")
		return
	}
	key := keys[0]

	url := googleOauthConfig.AuthCodeURL(googleRandomState + "|" + string(key))
	http.Redirect(res, req, url, http.StatusTemporaryRedirect)
}

func oauthGoogleUnlink(res http.ResponseWriter, req *http.Request) {
	// setting session data
	session, _ := store.Get(req, "boiler-session")

	constructedUserID, _ := primitive.ObjectIDFromHex(session.Values["id"].(string))

	if !session.Values["Google"].(bool) {
		res.WriteHeader(http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	_, unlinkError := DB.Collection("users").UpdateOne(ctx, bson.M{"_id": constructedUserID}, bson.M{"$set": bson.M{"Google": bson.M{}}})
	if unlinkError != nil {
		log.Println(unlinkError)
	}

	session.Values["Google"] = false
	err := sessions.Save(req, res)
	if err != nil {
		log.Printf("Error saving session: %v", err)
	}
	response, _ := json.Marshal(Response{true, "Successfully unlinked Google!"})
	res.Write(response)
	return
}

func oauthGoogleCallback(res http.ResponseWriter, req *http.Request) {
	// Read oauthState from Cookie

	if strings.Split(req.FormValue("state"), "|")[0] != googleRandomState {
		log.Println("invalid oauth google state")
		http.Redirect(res, req, "/", http.StatusTemporaryRedirect)
		return
	}

	data, err := getUserDataFromGoogle(req.FormValue("code"))
	if err != nil {
		log.Println(err.Error())
		http.Redirect(res, req, "/", http.StatusTemporaryRedirect)
		return
	}

	// sending an OTC to the user.
	// secured WebBrowser does not permit header modifications, and the
	// Google redirect drops external headers as well. Storing this one-time-use token
	// for the user to access with their main (axios) session.
	generatedOTC := RandStringRunes(15)

	// storing data
	ctx := context.Background()
	_, creationError := DB.Collection("cache").InsertOne(ctx, bson.M{"code": generatedOTC, "email": data.Email, "id": data.ID, "picture": data.Picture, "name": data.Name, "accessToken": req.FormValue("code")})
	if creationError != nil {
		log.Println("OTC Generation failed.")
		log.Println(creationError)
	}

	http.Redirect(res, req, strings.Split(req.FormValue("state"), "|")[1]+"provider=google&success=true&code="+generatedOTC, http.StatusTemporaryRedirect)
}

func oauthLink(res http.ResponseWriter, req *http.Request) {
	// decoding userdata
	decoder := json.NewDecoder(req.Body)
	var dec otc
	err := decoder.Decode(&dec)
	if err != nil {
		log.Panicln(err)
	}

	res.Header().Set("Content-Type", "application/json")

	// fetching cached data
	ctx := context.Background()
	foundCached := DB.Collection("cache").FindOne(ctx, bson.M{"code": dec.Code})
	var data oauthUserData
	decodeError := foundCached.Decode(&data)
	if decodeError != nil {
		log.Println(decodeError)
		log.Println("Cache fetch failed. Can not link user oauth.")
		response, _ := json.Marshal(Response{false, "Internal error."})
		res.WriteHeader(http.StatusInternalServerError)
		res.Write(response)
		return
	}

	// setting session data
	session, _ := store.Get(req, "boiler-session")

	foundUser := DB.Collection("users").FindOne(ctx, bson.M{"email": data.Email})

	var decodedFound user
	decodeError = foundUser.Decode(&decodedFound)

	foundUserWithToken := DB.Collection("users").FindOne(ctx, bson.M{"Google.id": data.ID})
	var decodedFoundUserWithToken user
	decodeErrorUserWithToken := foundUserWithToken.Decode(&decodedFoundUserWithToken)

	if decodeErrorUserWithToken == nil {
		if session.Values["auth"] != true {
			log.Println("Logging user in via Google OAuth.")
			session.Values["auth"] = true // now able to get users in the index page
			session.Values["id"] = decodedFoundUserWithToken.ID.String()
			session.Values["Google"] = true

			if decodedFoundUserWithToken.Password != "" {
				session.Values["hasPassword"] = true
			} else {
				session.Values["hasPassword"] = false
			}

			err := sessions.Save(req, res)
			if err != nil {
				log.Printf("Error saving session: %v", err)
			}
			response, _ := json.Marshal(Response{true, "Successfully logged in!"})
			res.Write(response)
			return
		}
		// user attempting to link account, but an user exists with this ID
		log.Println("This Google account is already linked.")
		response, _ := json.Marshal(Response{false, "This Google account is already linked."})
		res.WriteHeader(http.StatusBadRequest)
		res.Write(response)
		return
	}

	if session.Values["auth"] == true {
		// logged in. Attempting to link social account.
		ctx := context.Background()
		constructedUserID, _ := primitive.ObjectIDFromHex(session.Values["id"].(string))

		_, oauthLinkUpdateError := DB.Collection("users").UpdateOne(ctx, bson.M{"_id": constructedUserID}, bson.M{"$set": bson.M{"Google": bson.M{"id": data.ID, "accessToken": data.AccessToken}, "profile": bson.M{"name": data.Name, "picture": data.Picture}}})
		if oauthLinkUpdateError != nil {
			log.Println(oauthLinkUpdateError)
			log.Println("OAuth link failed. Internal server error.")
			response, _ := json.Marshal(Response{false, "OAuth link failed. Internal server error."})
			res.WriteHeader(http.StatusInternalServerError)
			res.Write(response)
			return
		}

		session.Values["Google"] = true
		err = sessions.Save(req, res)
		if err != nil {
			log.Printf("Error saving session: %v", err)
		}

		// sending a success response
		response, err := json.Marshal(Response{true, "Successfully linked!"})
		if err != nil {
			log.Println("Could not marshal response")
		}
		res.Write(response)
		return

	}

	// creating a new user if email is not taken...
	if decodeError == nil {
		log.Println("There is already an account associated with this email address.")
		response, _ := json.Marshal(Response{false, "There is already an account associated with this email address."})
		res.WriteHeader(http.StatusBadRequest)
		res.Write(response)
		return
	}

	// creating user...
	creationResult, creationError := DB.Collection("users").InsertOne(ctx, bson.M{"email": data.Email, "Google": bson.M{"id": data.ID, "accessToken": data.AccessToken}, "profile": bson.M{"name": data.Name, "picture": data.Picture}})
	log.Println(creationResult)

	if creationError != nil {
		log.Printf("Could not create new account. %s", creationError)
	}

	// setting session values
	session.Values["auth"] = true // now able to get users in the index page
	session.Values["id"] = creationResult.InsertedID.(primitive.ObjectID).Hex()
	session.Values["Google"] = true
	session.Values["hasPassword"] = false

	err = sessions.Save(req, res)
	if err != nil {
		log.Printf("Error saving session: %v", err)
	}

	// sending a success response
	response, err := json.Marshal(Response{true, "Successfully logged in!"})
	if err != nil {
		log.Println("Could not marshal response")
	}
	res.Write(response)
}

func getUserDataFromGoogle(code string) (result oauthUserData, e error) {
	// Use code to get token and get user info from Google.
	var receivedGoogleData oauthUserData

	token, err := googleOauthConfig.Exchange(context.Background(), code)
	if err != nil {
		return receivedGoogleData, fmt.Errorf("code exchange wrong: %s", err.Error())
	}

	response, err := http.Get(oauthGoogleURLAPI + token.AccessToken)
	if err != nil {
		return receivedGoogleData, fmt.Errorf("failed getting user info: %s", err.Error())
	}
	defer response.Body.Close()

	// retrieving user id
	decoder := json.NewDecoder(response.Body)
	decoder.Decode(&receivedGoogleData)

	if err != nil {
		return receivedGoogleData, fmt.Errorf("failed read response: %s", err.Error())
	}
	return receivedGoogleData, nil
}

func changePassword(res http.ResponseWriter, req *http.Request) {
	// decoding passwordchange data
	decoder := json.NewDecoder(req.Body)
	var postedPasswordChangeData passwordChangeData
	err := decoder.Decode(&postedPasswordChangeData)
	if err != nil {
		log.Panicln(err)
	}

	session, _ := store.Get(req, "boiler-session")

	// fetching user
	ctx := context.Background()
	constructedUserID, _ := primitive.ObjectIDFromHex(session.Values["id"].(string))
	foundUser := DB.Collection("users").FindOne(ctx, bson.M{"_id": constructedUserID})
	var u user
	decodeError := foundUser.Decode(&u)
	if decodeError != nil {
		log.Println(decodeError)
		log.Println("Password change failed. No user.")
		response, _ := json.Marshal(Response{false, "Invalid login details!"})
		res.WriteHeader(http.StatusUnauthorized)
		res.Write(response)
		return
	}

	// checking password
	comparisonError := bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(postedPasswordChangeData.OldPassword))
	if comparisonError != nil {
		log.Println("Password change failed. Incorrect old password.")
		response, _ := json.Marshal(Response{false, "Wrong old password!"})
		res.WriteHeader(http.StatusUnauthorized)
		res.Write(response)
		return
	}

	// hashing password
	hashed, _ := bcrypt.GenerateFromPassword([]byte(postedPasswordChangeData.NewPassword), 12)
	hashedConverted := string(hashed)

	// inserting user
	_, passwordChangeError := DB.Collection("users").UpdateOne(ctx, bson.M{"_id": constructedUserID}, bson.M{"$set": bson.M{"password": hashedConverted}})
	if passwordChangeError != nil {
		log.Println(passwordChangeError)
		log.Println("Password change failed. Internal server error.")
		response, _ := json.Marshal(Response{false, "Password change failed. Internal server error."})
		res.WriteHeader(http.StatusInternalServerError)
		res.Write(response)
		return
	}

	response, _ := json.Marshal(Response{true, "Password successfully changed!"})
	res.Write(response)
	return
}

func onlyUnauthorized(next http.Handler) http.Handler {
	return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		session, _ := store.Get(req, "boiler-session")
		if session.Values["auth"] != nil && session.Values["auth"].(bool) == true {
			res.WriteHeader(http.StatusBadRequest)
			return
		}

		next.ServeHTTP(res, req)
	})
}

func onlyAuthorized(next http.Handler) http.Handler {
	return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		session, _ := store.Get(req, "boiler-session")
		if session.Values["auth"] == nil || session.Values["auth"].(bool) == false {
			res.WriteHeader(http.StatusBadRequest)
			return
		}
		next.ServeHTTP(res, req)
	})
}
