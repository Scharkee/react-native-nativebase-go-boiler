import { take, put } from "redux-saga/effects";
import axios from "axios";
import * as mutations from "./mutations";
import { Alert } from "react-native";
import NavigationService from "../components/NavigationService";
import { fetchSession, storeSession } from "./localStorage";

const url = `http://10.0.2.2:8080`;

axios.interceptors.request.use(request => {
  console.log("Starting Request", request);
  return request;
});

export function* authenticationSaga() {
  while (true) {
    const { email, password } = yield take(mutations.REQUEST_AUTH);
    try {
      const { headers } = yield axios.post(`${url}/api/auth`, {
        email,
        password
      });

      // storing session locally.
      yield storeSession(headers["set-cookie"][0]);

      console.log(true);
      yield put(mutations.processAuth(mutations.AUTHENTICATED));

      // requesting people
      yield put({
        type: mutations.REQUEST_PEOPLE
      });

      // request profile, etc.
      NavigationService.navigate("Main");
    } catch (e) {
      console.log(e);
      yield put(mutations.processAuth(mutations.AUTH_ERROR));
    }
  }
}

export function* registrationSaga() {
  while (true) {
    const { email, password } = yield take(mutations.REQUEST_ACCOUNT_CREATION);
    try {
      yield axios.post(`${url}/api/register`, {
        email,
        password
      });
      yield put(mutations.processAuth(mutations.AUTHENTICATED));

      NavigationService.navigate("Main");
    } catch (e) {
      console.error(e);
      Alert.alert("Error", e.response.data.Msg);
      yield put(mutations.processAuth(mutations.AUTH_ERROR));
    }
  }
}

export function* sessionFetchSaga() {
  while (true) {
    yield take(mutations.REQUEST_SESSION_FETCH);
    try {
      // attempt to fetch local session token
      const storedSession = yield fetchSession();

      if (storedSession) {
        // set cookie
        axios.defaults.headers["set-cookie"] = storedSession;
      }

      const { data } = yield axios.get(`${url}/api/session`);

      yield put(
        mutations.processAuth(data.Auth ? mutations.AUTHENTICATED : null)
      );

      if (data.Auth) {
        yield put({
          type: mutations.REQUEST_PEOPLE
        });
        NavigationService.navigate("Main");
      }
    } catch (e) {
      console.error(e);
      Alert.alert("Error", "Couldn't reach server!");
    }
  }
}

export function* peopleFetchSaga() {
  while (true) {
    yield take(mutations.REQUEST_PEOPLE);
    try {
      const { data } = yield axios.get(`${url}/api/people`);
      yield put(mutations.setData({ people: data.People }));
    } catch (e) {
      console.error(e);
      Alert.alert("Error", "Couldn't fetch people!");
    }
  }
}

export function* logoutSaga() {
  while (true) {
    yield take(mutations.REQUEST_LOGOUT);
    try {
      yield axios.post(`${url}/api/logout`);
      yield put(mutations.clearData());
      yield put(mutations.processAuth(null));
      NavigationService.navigate("Home");
    } catch (e) {
      console.log(e);
      Alert.alert("Error", "Couldn't log out!");
    }
  }
}
