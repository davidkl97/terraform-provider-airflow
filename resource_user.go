package main

import (
	"fmt"
	"sync"

	"github.com/apache/airflow-client-go/airflow"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

var airflowUsers = map[string]airflow.UserCollectionItem{}
var airflowUsersFetch sync.Mutex

func resourceUser() *schema.Resource {
	return &schema.Resource{
		Create: resourceUserCreate,
		Read:   resourceUserRead,
		Update: resourceUserUpdate,
		Delete: resourceUserDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
		Schema: map[string]*schema.Schema{
			"active": {
				Type:     schema.TypeBool,
				Computed: true,
			},
			"email": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"failed_login_count": {
				Type:     schema.TypeInt,
				Computed: true,
			},
			"first_name": {
				Type:     schema.TypeString,
				Required: true,
			},
			"last_name": {
				Type:     schema.TypeString,
				Required: true,
			},
			"login_count": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"roles": {
				Type:     schema.TypeSet,
				Required: true,
				MinItems: 1,
				Elem:     &schema.Schema{Type: schema.TypeString},
			},
			"username": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"password": {
				Type:      schema.TypeString,
				Required:  true,
				Sensitive: true,
			},
		},
	}
}

func resourceUserCreate(d *schema.ResourceData, m interface{}) error {
	pcfg := m.(ProviderConfig)
	client := pcfg.ApiClient

	email := d.Get("email").(string)
	firstName := d.Get("first_name").(string)
	lastName := d.Get("last_name").(string)
	username := d.Get("username").(string)
	password := d.Get("password").(string)
	roles := expandAirflowUserRoles(d.Get("roles").(*schema.Set))

	userApi := client.UserApi

	_, _, err := userApi.PostUser(pcfg.AuthContext).User(airflow.User{
		Email:     &email,
		FirstName: &firstName,
		LastName:  &lastName,
		Username:  &username,
		Password:  &password,
		Roles:     &roles,
	}).Execute()
	if err != nil {
		return fmt.Errorf("failed to create user `%s` from Airflow: %w", email, err)
	}

	// Set e-mail as resource Id. It is unique just like the user username.
	// However, in some cases like on GCP Cloud Composer, the username may
	// be changed by an external auth layer. This will conflict with the
	// Terraform state so it's safer to use the e-mail as the Id.
	d.SetId(email)

	return resourceUserRead(d, m)
}

func fetchAllUsers(users map[string]airflow.UserCollectionItem, offset int32, m interface{}) error {
	pcfg := m.(ProviderConfig)
	client := pcfg.ApiClient
	// This is the Airflow API default maximum page size.
	limit := int32(100)

	usersInResponse, resp, err := client.UserApi.GetUsers(pcfg.AuthContext).Limit(limit).Offset(offset).Execute()
	if resp != nil && err == nil {
		for _, u := range usersInResponse.GetUsers() {
			users[*u.Email] = u
		}
	}

	if err != nil {
		return err
	}

	// Recurse to the next page in case there are more users to fetch.
	if *usersInResponse.TotalEntries > int32(len(users)) {
		return fetchAllUsers(users, offset+limit, m)
	}

	return nil
}

func resourceUserRead(d *schema.ResourceData, m interface{}) error {
	// Use a lock to prevent concurrent map access.
	airflowUsersFetch.Lock()
	
	err := fetchAllUsers(airflowUsers, 0, m)
	if err != nil {
		airflowUsersFetch.Unlock()
		return fmt.Errorf("failed to get all users from Airflow: %w", err)
	}
	user, exists := airflowUsers[d.Id()]
	airflowUsersFetch.Unlock()

	if !exists {
		d.SetId("")
		return nil
	}
	
	d.Set("active", user.GetActive())
	d.Set("email", user.Email)
	d.Set("failed_login_count", user.GetFailedLoginCount())
	d.Set("first_name", user.FirstName)
	d.Set("last_name", user.LastName)
	d.Set("login_count", user.GetLastLogin())
	d.Set("username", user.Username)
	d.Set("password", d.Get("password").(string))
	d.Set("roles", flattenAirflowUserRoles(*user.Roles))

	return nil
}

func resourceUserUpdate(d *schema.ResourceData, m interface{}) error {
	pcfg := m.(ProviderConfig)
	client := pcfg.ApiClient

	email := d.Id()
	firstName := d.Get("first_name").(string)
	lastName := d.Get("last_name").(string)
	password := d.Get("password").(string)
	roles := expandAirflowUserRoles(d.Get("roles").(*schema.Set))
	username := d.Get("username").(string)

	// Do use username and not the resource Id (=e-mail) when making API calls.
	_, _, err := client.UserApi.PatchUser(pcfg.AuthContext, username).User(airflow.User{
		Email:     &email,
		FirstName: &firstName,
		LastName:  &lastName,
		Password:  &password,
		Roles:     &roles,
		Username:  &username,
	}).Execute()
	if err != nil {
		return fmt.Errorf("failed to update user `%s` from Airflow: %w", email, err)
	}

	return resourceUserRead(d, m)
}

func resourceUserDelete(d *schema.ResourceData, m interface{}) error {
	pcfg := m.(ProviderConfig)
	client := pcfg.ApiClient
	username := d.Get("username").(string)

	// Do use username and not the resource Id (=e-mail) when making API calls.
	resp, err := client.UserApi.DeleteUser(pcfg.AuthContext, username).Execute()
	if err != nil {
		return fmt.Errorf("failed to delete user `%s` from Airflow: %w", d.Id(), err)
	}

	if resp != nil && resp.StatusCode == 404 {
		return nil
	}

	return nil
}

func expandAirflowUserRoles(tfList *schema.Set) []airflow.UserCollectionItemRoles {
	if tfList.Len() == 0 {
		return nil
	}

	apiObjects := make([]airflow.UserCollectionItemRoles, 0)

	for _, tfMapRaw := range tfList.List() {
		val, ok := tfMapRaw.(string)

		if !ok {
			continue
		}

		apiObject := airflow.UserCollectionItemRoles{
			Name: &val,
		}
		apiObjects = append(apiObjects, apiObject)
	}

	return apiObjects
}

func flattenAirflowUserRoles(apiObjects []airflow.UserCollectionItemRoles) []string {
	vs := make([]string, 0, len(apiObjects))
	for _, v := range apiObjects {
		name := *v.Name
		vs = append(vs, name)
	}
	return vs
}
