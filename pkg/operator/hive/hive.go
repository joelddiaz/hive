package hive

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	oappsv1 "github.com/openshift/api/apps/v1"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"

	hivev1 "github.com/openshift/hive/pkg/apis/hive/v1"
	"github.com/openshift/hive/pkg/constants"
	hiveconstants "github.com/openshift/hive/pkg/constants"
	"github.com/openshift/hive/pkg/controller/images"
	"github.com/openshift/hive/pkg/controller/utils"
	"github.com/openshift/hive/pkg/operator/assets"
	"github.com/openshift/hive/pkg/operator/util"
	"github.com/openshift/hive/pkg/resource"
)

const (
	dnsServersEnvVar = "ZONE_CHECK_DNS_SERVERS"

	// hiveAdditionalCASecret is the name of the secret in the hive namespace
	// that will contain the aggregate of all AdditionalCertificateAuthorities
	// secrets specified in HiveConfig
	hiveAdditionalCASecret = "hive-additional-ca"

	// hiveControllersConfigMapName is the name of the configmap to store the
	// configurations like goroutines, qps, burst etc. for different hive controllers
	hiveControllersConfigMapName = "hive-controllers-config"

	// hiveConfigHashAnnotation is annotation on hivedeployment that contains
	// the hash of the contents of the hive-controllers-config configmap
	hiveConfigHashAnnotation = "hive.openshift.io/hiveconfig-hash"
)

func (r *ReconcileHiveConfig) deployHive(hLog log.FieldLogger, h resource.Helper, instance *hivev1.HiveConfig, recorder events.Recorder, mdConfigMap *corev1.ConfigMap) error {

	asset := assets.MustAsset("config/controllers/deployment.yaml")
	hLog.Debug("reading deployment")
	hiveDeployment := resourceread.ReadDeploymentV1OrDie(asset)
	hiveContainer := &hiveDeployment.Spec.Template.Spec.Containers[0]

	hLog.Infof("hive image: %s", r.hiveImage)
	if r.hiveImage != "" {
		hiveContainer.Image = r.hiveImage
		hiveImageEnvVar := corev1.EnvVar{
			Name:  images.HiveImageEnvVar,
			Value: r.hiveImage,
		}

		hiveContainer.Env = append(hiveContainer.Env, hiveImageEnvVar)
	}

	if r.hiveImagePullPolicy != "" {
		hiveContainer.ImagePullPolicy = r.hiveImagePullPolicy

		hiveContainer.Env = append(
			hiveContainer.Env,
			corev1.EnvVar{
				Name:  images.HiveImagePullPolicyEnvVar,
				Value: string(r.hiveImagePullPolicy),
			},
		)
	}

	if dc := instance.Spec.DisabledControllers; len(dc) != 0 {
		hiveContainer.Args = append(hiveContainer.Args, "--disabled-controllers", strings.Join(dc, ","))
	}

	if level := instance.Spec.LogLevel; level != "" {
		hiveContainer.Args = append(hiveContainer.Args, "--log-level", level)
	}

	if syncSetReapplyInterval := instance.Spec.SyncSetReapplyInterval; syncSetReapplyInterval != "" {
		syncsetReapplyIntervalEnvVar := corev1.EnvVar{
			Name:  "SYNCSET_REAPPLY_INTERVAL",
			Value: syncSetReapplyInterval,
		}

		hiveContainer.Env = append(hiveContainer.Env, syncsetReapplyIntervalEnvVar)
	}

	addManagedDomainsVolume(&hiveDeployment.Spec.Template.Spec, mdConfigMap.Name)

	hiveNSName := getHiveNamespace(instance)

	// By default we will try to gather logs on failed installs:
	logsEnvVar := corev1.EnvVar{
		Name:  constants.SkipGatherLogsEnvVar,
		Value: strconv.FormatBool(instance.Spec.FailedProvisionConfig.SkipGatherLogs),
	}
	hiveContainer.Env = append(hiveContainer.Env, logsEnvVar)

	if zoneCheckDNSServers := os.Getenv(dnsServersEnvVar); len(zoneCheckDNSServers) > 0 {
		dnsServersEnvVar := corev1.EnvVar{
			Name:  dnsServersEnvVar,
			Value: zoneCheckDNSServers,
		}
		hiveContainer.Env = append(hiveContainer.Env, dnsServersEnvVar)
	}

	if instance.Spec.Backup.Velero.Enabled {
		hLog.Infof("Velero Backup Enabled.")
		tmpEnvVar := corev1.EnvVar{
			Name:  hiveconstants.VeleroBackupEnvVar,
			Value: "true",
		}
		hiveContainer.Env = append(hiveContainer.Env, tmpEnvVar)

		if instance.Spec.Backup.Velero.Namespace != "" {
			hLog.Infof("Velero Backup Namespace specified.")
			tmpEnvVar := corev1.EnvVar{
				Name:  hiveconstants.VeleroNamespaceEnvVar,
				Value: instance.Spec.Backup.Velero.Namespace,
			}
			hiveContainer.Env = append(hiveContainer.Env, tmpEnvVar)
		}
	}

	if instance.Spec.DeprovisionsDisabled != nil && *instance.Spec.DeprovisionsDisabled {
		hLog.Info("deprovisions disabled in hiveconfig")
		tmpEnvVar := corev1.EnvVar{
			Name:  hiveconstants.DeprovisionsDisabledEnvVar,
			Value: "true",
		}
		hiveContainer.Env = append(hiveContainer.Env, tmpEnvVar)
	}

	if instance.Spec.Backup.MinBackupPeriodSeconds != nil {
		hLog.Infof("MinBackupPeriodSeconds specified.")
		tmpEnvVar := corev1.EnvVar{
			Name:  hiveconstants.MinBackupPeriodSecondsEnvVar,
			Value: strconv.Itoa(*instance.Spec.Backup.MinBackupPeriodSeconds),
		}
		hiveContainer.Env = append(hiveContainer.Env, tmpEnvVar)
	}

	if instance.Spec.DeleteProtection == hivev1.DeleteProtectionEnabled {
		hLog.Info("Delete Protection enabled")
		hiveContainer.Env = append(hiveContainer.Env, corev1.EnvVar{
			Name:  hiveconstants.ProtectedDeleteEnvVar,
			Value: "true",
		})
	}

	if err := r.includeAdditionalCAs(hLog, h, instance, hiveDeployment); err != nil {
		return err
	}

	r.includeGlobalPullSecret(hLog, h, instance, hiveDeployment)

	if instance.Spec.MaintenanceMode != nil && *instance.Spec.MaintenanceMode {
		hLog.Warn("maintenanceMode enabled in HiveConfig, setting hive-controllers replicas to 0")
		replicas := int32(0)
		hiveDeployment.Spec.Replicas = &replicas
	}

	hiveControllersConfigMap := &corev1.ConfigMap{}
	hiveControllersConfigMap.Name = hiveControllersConfigMapName
	hiveControllersConfigMap.Namespace = hiveNSName
	hiveControllersConfigMap.Data = make(map[string]string)

	if instance.Spec.ControllersConfig != nil {
		if instance.Spec.ControllersConfig.Default != nil {
			setHiveControllersConfig(instance.Spec.ControllersConfig.Default, hiveControllersConfigMap, "default")
		}
		for _, controller := range instance.Spec.ControllersConfig.Controllers {
			setHiveControllersConfig(&controller.Config, hiveControllersConfigMap, controller.Name)
		}
	}

	result, err := util.ApplyRuntimeObjectWithGC(h, hiveControllersConfigMap, instance)
	if err != nil {
		hLog.WithError(err).Error("error applying hive-controllers-config configmap")
		return err
	}
	hLog.WithField("result", result).Info("hive-controllers-config configmap applied")

	hLog.Info("Hashing hive-controllers-config data onto a hive deployment annotation")
	hiveControllersConfigHash := computeHiveControllersConfigHash(hiveControllersConfigMap)
	if hiveDeployment.Spec.Template.Annotations == nil {
		hiveDeployment.Spec.Template.Annotations = make(map[string]string, 1)
	}
	hiveDeployment.Spec.Template.Annotations[hiveConfigHashAnnotation] = hiveControllersConfigHash

	// Load namespaced assets, decode them, set to our target namespace, and apply:
	namespacedAssets := []string{
		"config/controllers/service.yaml",
		"config/configmaps/install-log-regexes-configmap.yaml",
		"config/rbac/hive_backend_serviceaccount.yaml",
		"config/rbac/hive_frontend_serviceaccount.yaml",
		"config/controllers/hive_controllers_serviceaccount.yaml",
	}
	for _, assetPath := range namespacedAssets {
		if err := util.ApplyAssetWithNSOverrideAndGC(h, assetPath, hiveNSName, instance); err != nil {
			hLog.WithError(err).Error("error applying object with namespace override")
			return err
		}
		hLog.WithField("asset", assetPath).Info("applied asset with namespace override")
	}

	// Apply global non-namespaced assets:
	applyAssets := []string{
		"config/rbac/hive_backend_role.yaml",
		"config/rbac/hive_frontend_role.yaml",
		"config/controllers/hive_controllers_role.yaml",
	}
	for _, a := range applyAssets {
		if err := util.ApplyAssetWithGC(h, a, instance, hLog); err != nil {
			hLog.WithField("asset", a).WithError(err).Error("error applying asset")
			return err
		}
	}

	// Apply global ClusterRoleBindings which may need Subject namespace overrides for their ServiceAccounts.
	clusterRoleBindingAssets := []string{
		"config/rbac/hive_backend_role_binding.yaml",
		"config/rbac/hive_frontend_role_binding.yaml",
		"config/controllers/hive_controllers_role_binding.yaml",
	}
	for _, crbAsset := range clusterRoleBindingAssets {

		if err := util.ApplyClusterRoleBindingAssetWithSubjectNSOverrideAndGC(h, crbAsset, hiveNSName, instance); err != nil {
			hLog.WithError(err).Error("error applying ClusterRoleBinding with namespace override")
			return err
		}
		hLog.WithField("asset", crbAsset).Info("applied ClusterRoleRoleBinding asset with namespace override")
	}

	// In very rare cases we use OpenShift specific types which will not apply if running on
	// vanilla Kubernetes. Detect this and skip if so.
	// NOTE: We are configuring two role bindings here, but they do not use namespaced ServiceAccount subjects.
	// (rather global OpenShift groups), thus they do not need namespace override behavior.
	openshiftSpecificAssets := []string{
		"config/rbac/hive_admin_role.yaml",
		"config/rbac/hive_reader_role.yaml",
		"config/rbac/hive_admin_role_binding.yaml",
		"config/rbac/hive_reader_role_binding.yaml",
	}
	isOpenShift, err := r.runningOnOpenShift(hLog)
	if err != nil {
		return err
	}
	if isOpenShift {
		hLog.Info("deploying OpenShift specific assets")
		for _, a := range openshiftSpecificAssets {
			err = util.ApplyAssetWithGC(h, a, instance, hLog)
			if err != nil {
				return err
			}
		}
	} else {
		hLog.Warn("hive is not running on OpenShift, some optional assets will not be deployed")
	}

	hiveDeployment.Namespace = hiveNSName
	result, err = util.ApplyRuntimeObjectWithGC(h, hiveDeployment, instance)
	if err != nil {
		hLog.WithError(err).Error("error applying deployment")
		return err
	}
	hLog.Infof("hive-controllers deployment applied (%s)", result)

	hLog.Info("all hive components successfully reconciled")
	return nil
}

func (r *ReconcileHiveConfig) includeAdditionalCAs(hLog log.FieldLogger, h resource.Helper, instance *hivev1.HiveConfig, hiveDeployment *appsv1.Deployment) error {
	additionalCA := &bytes.Buffer{}
	for _, clientCARef := range instance.Spec.AdditionalCertificateAuthoritiesSecretRef {
		caSecret := &corev1.Secret{}
		err := r.Get(context.TODO(), types.NamespacedName{Namespace: getHiveNamespace(instance), Name: clientCARef.Name}, caSecret)
		if err != nil {
			hLog.WithError(err).WithField("secret", clientCARef.Name).Errorf("Cannot read client CA secret")
			continue
		}
		crt, ok := caSecret.Data["ca.crt"]
		if !ok {
			hLog.WithField("secret", clientCARef.Name).Warning("Secret does not contain expected key (ca.crt)")
		}
		fmt.Fprintf(additionalCA, "%s\n", crt)
	}

	if additionalCA.Len() == 0 {
		caSecret := &corev1.Secret{}
		err := r.Get(context.TODO(), types.NamespacedName{Namespace: getHiveNamespace(instance), Name: hiveAdditionalCASecret}, caSecret)
		if err == nil {
			err = r.Delete(context.TODO(), caSecret)
			if err != nil {
				hLog.WithError(err).WithField("secret", fmt.Sprintf("%s/%s", getHiveNamespace(instance), hiveAdditionalCASecret)).
					Error("cannot delete hive additional ca secret")
				return err
			}
		}
		return nil
	}

	caSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: getHiveNamespace(instance),
			Name:      hiveAdditionalCASecret,
		},
		Data: map[string][]byte{
			"ca.crt": additionalCA.Bytes(),
		},
	}
	result, err := util.ApplyRuntimeObjectWithGC(h, caSecret, instance)
	if err != nil {
		hLog.WithError(err).Error("error applying additional cert secret")
		return err
	}
	hLog.Infof("additional cert secret applied (%s)", result)

	// Generating a volume name with a hash based on the contents of the additional CA
	// secret will ensure that when there are changes to the secret, the hive controller
	// will be re-deployed.
	hash := fmt.Sprintf("%x", md5.Sum(additionalCA.Bytes()))
	volumeName := fmt.Sprintf("additionalca-%s", hash[:20])

	hiveDeployment.Spec.Template.Spec.Volumes = append(hiveDeployment.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: volumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: hiveAdditionalCASecret,
			},
		},
	})

	hiveDeployment.Spec.Template.Spec.Containers[0].VolumeMounts = append(hiveDeployment.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
		Name:      volumeName,
		MountPath: "/additional/ca",
		ReadOnly:  true,
	})

	hiveDeployment.Spec.Template.Spec.Containers[0].Env = append(hiveDeployment.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{
		Name:  "ADDITIONAL_CA",
		Value: "/additional/ca/ca.crt",
	})

	return nil
}

func (r *ReconcileHiveConfig) includeGlobalPullSecret(hLog log.FieldLogger, h resource.Helper, instance *hivev1.HiveConfig, hiveDeployment *appsv1.Deployment) {
	if instance.Spec.GlobalPullSecretRef == nil || instance.Spec.GlobalPullSecretRef.Name == "" {
		hLog.Debug("GlobalPullSecret is not provided in HiveConfig, it will not be deployed")
		return
	}

	globalPullSecretEnvVar := corev1.EnvVar{
		Name:  hiveconstants.GlobalPullSecret,
		Value: instance.Spec.GlobalPullSecretRef.Name,
	}
	hiveDeployment.Spec.Template.Spec.Containers[0].Env = append(hiveDeployment.Spec.Template.Spec.Containers[0].Env, globalPullSecretEnvVar)
}

func (r *ReconcileHiveConfig) runningOnOpenShift(hLog log.FieldLogger) (bool, error) {
	deploymentConfigGroupVersion := oappsv1.GroupVersion.String()
	list, err := r.discoveryClient.ServerResourcesForGroupVersion(deploymentConfigGroupVersion)
	if err != nil {
		if apierrors.IsNotFound(err) {
			hLog.WithError(err).Debug("DeploymentConfig objects not found, not running on OpenShift")
			return false, nil
		}
		hLog.WithError(err).Error("Error determining whether running on OpenShift")
		return false, err
	}

	return len(list.APIResources) > 0, nil
}

func (r *ReconcileHiveConfig) cleanupLegacySyncSetInstances(hLog log.FieldLogger) error {
	crdClient := r.dynamicClient.Resource(schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1beta1",
		Resource: "customresourcedefinitions",
	})
	syncSetInstanceCRD, err := crdClient.Get(context.Background(), "syncsetinstances.hive.openshift.io", metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		hLog.Debug("syncsetinstance crd has already been deleted")
		return nil
	case err != nil:
		return errors.Wrap(err, "could not get the syncsetinstance CRD")
	}
	// Delete all the SyncSetInstance. List until there are no more SyncSetInstances to catch SyncSetInstances that may
	// have been created since the last List was run. There should not be more SyncSetInstance created since the Hive
	// controller that would create SyncSetInstances should not be running, but let's List until there is a zero count
	// just in case.
	for {
		numberDeleted, err := r.deleteAllSyncSetInstances(hLog)
		if err != nil {
			return err
		}
		if numberDeleted == 0 {
			break
		}
	}
	hLog.Info("Deleting SyncSetInstance CRD")
	if err := crdClient.Delete(context.Background(), syncSetInstanceCRD.GetName(), metav1.DeleteOptions{}); err != nil {
		return errors.Wrap(err, "failed to delete syncsetinstance CRD")
	}
	return nil
}

func (r *ReconcileHiveConfig) deleteAllSyncSetInstances(hLog log.FieldLogger) (numberDeleted int, returnErr error) {
	syncSetInstanceClient := r.dynamicClient.Resource(hivev1.SchemeGroupVersion.WithResource("syncsetinstances"))
	hLog.Info("deleting SyncSetInstances")
	listOptions := metav1.ListOptions{}
	for {
		syncSetInstanceList, err := syncSetInstanceClient.List(context.Background(), listOptions)
		if err != nil {
			return numberDeleted, errors.Wrap(err, "failed to list SyncSetInstances")
		}
		hLog.WithField("numberDeleted", numberDeleted).WithField("batchSize", len(syncSetInstanceList.Items)).Infof("deleting the next batch of SyncSetInstances")
		var errs []error
		for _, syncSetInstance := range syncSetInstanceList.Items {
			c := syncSetInstanceClient.Namespace(syncSetInstance.GetNamespace())
			resourceVersion := syncSetInstance.GetResourceVersion()
			if len(syncSetInstance.GetFinalizers()) != 0 {
				syncSetInstance.SetFinalizers(nil)
				updatedSyncSetInstance, err := c.Update(context.Background(), &syncSetInstance, metav1.UpdateOptions{})
				if err != nil {
					errs = append(errs, errors.Wrapf(err, "failed to remove finalizers from SyncSetInstance %s/%s", syncSetInstance.GetNamespace(), syncSetInstance.GetName()))
					continue
				}
				resourceVersion = updatedSyncSetInstance.GetResourceVersion()
			}
			// Ensure that we are deleting the SyncSetInstance version to which we just updated. In case the Hive
			// syncsetinstance controller is still running, this will protect against the controller putting back the
			// finalizer between when we removed the finalizers and when we did the delete. If the controller is running
			// and puts back the finalizer, then the controller may attempt to delete synced resources in the target
			// cluster.
			uid := syncSetInstance.GetUID()
			switch err := c.Delete(
				context.Background(),
				syncSetInstance.GetName(),
				metav1.DeleteOptions{
					Preconditions: &metav1.Preconditions{
						UID:             &uid,
						ResourceVersion: &resourceVersion,
					},
				},
			); {
			case err == nil, apierrors.IsNotFound(err):
				numberDeleted++
			default:
				errs = append(errs, errors.Wrapf(err, "failed to delete SyncSetInstance %s/%s", syncSetInstance.GetNamespace(), syncSetInstance.GetName()))
			}
		}
		if len(errs) != 0 {
			return numberDeleted, utilerrors.NewAggregate(errs)
		}
		cont := syncSetInstanceList.GetContinue()
		if cont == "" {
			break
		}
		listOptions.Continue = cont
	}
	return
}

func computeHiveControllersConfigHash(hiveControllersConfigMap *corev1.ConfigMap) string {
	hasher := md5.New()
	hasher.Write([]byte(fmt.Sprintf("%v", hiveControllersConfigMap.Data)))
	return hex.EncodeToString(hasher.Sum(nil))
}

func setHiveControllersConfig(config *hivev1.ControllerConfig, hiveControllersConfigMap *corev1.ConfigMap, controllerName hivev1.ControllerName) {
	if config.ConcurrentReconciles != nil {
		hiveControllersConfigMap.Data[fmt.Sprintf(utils.ConcurrentReconcilesEnvVariableFormat, controllerName)] = strconv.Itoa(int(*config.ConcurrentReconciles))
	}
	if config.ClientQPS != nil {
		hiveControllersConfigMap.Data[fmt.Sprintf(utils.ClientQPSEnvVariableFormat, controllerName)] = strconv.Itoa(int(*config.ClientQPS))
	}
	if config.ClientBurst != nil {
		hiveControllersConfigMap.Data[fmt.Sprintf(utils.ClientBurstEnvVariableFormat, controllerName)] = strconv.Itoa(int(*config.ClientBurst))
	}
	if config.QueueQPS != nil {
		hiveControllersConfigMap.Data[fmt.Sprintf(utils.QueueQPSEnvVariableFormat, controllerName)] = strconv.Itoa(int(*config.QueueQPS))
	}
	if config.QueueBurst != nil {
		hiveControllersConfigMap.Data[fmt.Sprintf(utils.QueueBurstEnvVariableFormat, controllerName)] = strconv.Itoa(int(*config.QueueBurst))
	}
}
